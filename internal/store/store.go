// Package store persists the small amount of server state in files.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/r3t2/ezgamelan/internal/atomicfile"
	"github.com/r3t2/ezgamelan/internal/invite"
)

const MaxClients = 10

var (
	ErrNotFound          = errors.New("not found")
	ErrInviteExists      = errors.New("client already exists")
	ErrClientLimit       = errors.New("client limit reached")
	ErrInvalidToken      = errors.New("token is invalid or expired")
	ErrRevocationPending = errors.New("revocation recorded but publication is pending")
)

type ClientStatus string

const (
	StatusPending ClientStatus = "pending"
	StatusActive  ClientStatus = "active"
	StatusRevoked ClientStatus = "revoked"
)

type Client struct {
	Name      string       `json:"name"`
	SerialHex string       `json:"serial_hex"`
	Status    ClientStatus `json:"status"`
	CreatedAt time.Time    `json:"created_at"`
	NotAfter  time.Time    `json:"not_after"`
	RevokedAt *time.Time   `json:"revoked_at,omitempty"`
}

type Pending struct {
	FormatVersion int            `json:"format_version"`
	Name          string         `json:"name"`
	TokenHash     string         `json:"token_hash"`
	ExpiresAt     time.Time      `json:"expires_at"`
	Profile       invite.Profile `json:"profile"`
}

type Revocation struct {
	Name      string    `json:"name"`
	SerialHex string    `json:"serial_hex"`
	RevokedAt time.Time `json:"revoked_at"`
}

type Store struct {
	root            string
	clientsDir      string
	pendingDir      string
	lockPath        string
	revocationsPath string
}

func Open(root string) (*Store, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("state directory must be absolute")
	}
	s := &Store{
		root:            root,
		clientsDir:      filepath.Join(root, "clients"),
		pendingDir:      filepath.Join(root, "pending"),
		lockPath:        filepath.Join(root, ".lock"),
		revocationsPath: filepath.Join(root, "revocations.json"),
	}
	for _, dir := range []string{s.root, s.clientsDir, s.pendingDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create state directory %q: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("secure state directory %q: %w", dir, err)
		}
	}
	return s, nil
}

func (s *Store) AddPending(client Client, pending Pending, now time.Time) error {
	return s.withLock(func() error {
		if err := s.cleanupExpiredUnlocked(now); err != nil {
			return err
		}
		if err := validateNewClient(client); err != nil {
			return err
		}
		if pending.FormatVersion != 1 || pending.Name != client.Name ||
			pending.TokenHash == "" || !pending.ExpiresAt.After(now) {
			return errors.New("pending invite is invalid")
		}
		if err := pending.Profile.Validate(now); err != nil {
			return fmt.Errorf("validate pending profile: %w", err)
		}
		replace, err := s.prepareClientNameUnlocked(client.Name)
		if err != nil {
			return err
		}
		count, err := s.countCurrentUnlocked()
		if err != nil {
			return err
		}
		if count >= MaxClients {
			return ErrClientLimit
		}

		var previous Client
		if replace {
			previous, err = s.readClientUnlocked(client.Name)
			if err != nil {
				return err
			}
		}
		client.Status = StatusPending
		if err := s.writeNewClientUnlocked(client, replace); err != nil {
			return err
		}
		if err := writeJSONCreate(s.pendingPath(client.Name), pending, 0o600); err != nil {
			if replace {
				_ = writeJSONReplace(s.clientPath(client.Name), previous, 0o600)
			} else {
				_ = os.Remove(s.clientPath(client.Name))
			}
			return err
		}
		return nil
	})
}

// Claim consumes a token before returning the profile. A failed transfer can
// be recovered by revoking the client and issuing a new invite.
func (s *Store) Claim(token string, now time.Time) (invite.Profile, error) {
	if err := invite.ValidateToken(token); err != nil {
		return invite.Profile{}, ErrInvalidToken
	}
	var result invite.Profile
	err := s.withLock(func() error {
		if err := s.cleanupExpiredUnlocked(now); err != nil {
			return err
		}
		entries, err := os.ReadDir(s.pendingDir)
		if err != nil {
			return fmt.Errorf("read pending invites: %w", err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			var pending Pending
			if err := readJSONStrict(filepath.Join(s.pendingDir, entry.Name()), &pending, invite.MaxSize*2); err != nil {
				return err
			}
			if !invite.MatchToken(pending.TokenHash, token) {
				continue
			}
			if !invite.ValidName(pending.Name) {
				return errors.New("pending invite contains invalid client name")
			}
			client, err := s.readClientUnlocked(pending.Name)
			if err != nil {
				return err
			}
			if client.Status != StatusPending || !pending.ExpiresAt.After(now) {
				return ErrInvalidToken
			}
			revocations, err := s.readRevocationsUnlocked()
			if err != nil {
				return err
			}
			for _, record := range revocations {
				if record.SerialHex == client.SerialHex {
					return ErrInvalidToken
				}
			}
			client.Status = StatusActive
			if err := writeJSONReplace(s.clientPath(client.Name), client, 0o600); err != nil {
				return err
			}
			if err := os.Remove(s.pendingPath(client.Name)); err != nil {
				return fmt.Errorf("consume pending invite: %w", err)
			}
			if err := syncDirectory(s.pendingDir); err != nil {
				return err
			}
			result = pending.Profile
			return nil
		}
		return ErrInvalidToken
	})
	return result, err
}

func (s *Store) List(now time.Time) ([]Client, error) {
	var result []Client
	err := s.withLock(func() error {
		if err := s.cleanupExpiredUnlocked(now); err != nil {
			return err
		}
		var err error
		result, err = s.listUnlocked()
		return err
	})
	return result, err
}

// Revoke persists the revocation before publishing it to OpenVPN.
func (s *Store) Revoke(
	name string,
	now time.Time,
	writeRevocations func([]Revocation) error,
) error {
	if !invite.ValidName(name) {
		return errors.New("client name is invalid")
	}
	return s.withLock(func() error {
		if err := s.cleanupExpiredUnlocked(now); err != nil {
			return err
		}
		client, err := s.readClientUnlocked(name)
		if err != nil {
			return err
		}
		revocations, err := s.readRevocationsUnlocked()
		if err != nil {
			return err
		}
		found := false
		for _, item := range revocations {
			if item.SerialHex == client.SerialHex {
				found = true
				break
			}
		}
		if !found {
			revocations = append(revocations, Revocation{
				Name:      client.Name,
				SerialHex: client.SerialHex,
				RevokedAt: now,
			})
		}
		if err := writeJSONReplace(s.revocationsPath, revocations, 0o600); err != nil {
			return err
		}
		if err := writeRevocations(revocations); err != nil {
			return errors.Join(ErrRevocationPending, err)
		}
		return s.applyRevocationsUnlocked(revocations)
	})
}

func (s *Store) Revocations() ([]Revocation, error) {
	var records []Revocation
	err := s.withLock(func() error {
		var err error
		records, err = s.readRevocationsUnlocked()
		return err
	})
	return records, err
}

// ReconcileRevocations merges verified external records and publishes the ledger
// under the same lock used by invitations and revocations.
func (s *Store) ReconcileRevocations(readExternal func() ([]Revocation, error), publish func([]Revocation) error) error {
	return s.withLock(func() error {
		records, err := s.readRevocationsUnlocked()
		if err != nil {
			return err
		}
		external, err := readExternal()
		if err != nil {
			return err
		}
		seen := make(map[string]bool, len(records))
		for _, record := range records {
			seen[record.SerialHex] = true
		}
		changed := false
		for _, record := range external {
			if !seen[record.SerialHex] {
				records = append(records, record)
				seen[record.SerialHex] = true
				changed = true
			}
		}
		if changed {
			if err := writeJSONReplace(s.revocationsPath, records, 0o600); err != nil {
				return err
			}
		}
		if err := publish(records); err != nil {
			return err
		}
		return s.applyRevocationsUnlocked(records)
	})
}

func (s *Store) applyRevocationsUnlocked(records []Revocation) error {
	clients, err := s.listUnlocked()
	if err != nil {
		return err
	}
	bySerial := make(map[string]time.Time, len(records))
	for _, record := range records {
		bySerial[record.SerialHex] = record.RevokedAt
	}
	for _, client := range clients {
		at, revoked := bySerial[client.SerialHex]
		if !revoked {
			continue
		}
		if client.Status != StatusRevoked || client.RevokedAt == nil {
			client.Status = StatusRevoked
			client.RevokedAt = &at
			if err := writeJSONReplace(s.clientPath(client.Name), client, 0o600); err != nil {
				return err
			}
		}
		if err := os.Remove(s.pendingPath(client.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return syncDirectory(s.pendingDir)
}

func (s *Store) cleanupExpiredUnlocked(now time.Time) error {
	entries, err := os.ReadDir(s.pendingDir)
	if err != nil {
		return fmt.Errorf("read pending invites: %w", err)
	}
	changed := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.pendingDir, entry.Name())
		var pending Pending
		if err := readJSONStrict(path, &pending, invite.MaxSize*2); err != nil {
			return err
		}
		if pending.ExpiresAt.After(now) {
			continue
		}
		if !invite.ValidName(pending.Name) {
			return errors.New("pending invite contains invalid client name")
		}
		client, err := s.readClientUnlocked(pending.Name)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err == nil && client.Status == StatusPending {
			if err := os.Remove(s.clientPath(client.Name)); err != nil {
				return fmt.Errorf("remove expired client: %w", err)
			}
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove expired invite: %w", err)
		}
		changed = true
	}
	if changed {
		if err := syncDirectory(s.clientsDir); err != nil {
			return err
		}
		if err := syncDirectory(s.pendingDir); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) countCurrentUnlocked() (int, error) {
	clients, err := s.listUnlocked()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, client := range clients {
		if client.Status == StatusPending || client.Status == StatusActive {
			count++
		}
	}
	return count, nil
}

func (s *Store) prepareClientNameUnlocked(name string) (bool, error) {
	client, err := s.readClientUnlocked(name)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if client.Status != StatusRevoked {
		return false, ErrInviteExists
	}
	return true, nil
}

func (s *Store) writeNewClientUnlocked(client Client, replace bool) error {
	if replace {
		return writeJSONReplace(s.clientPath(client.Name), client, 0o600)
	}
	return writeJSONCreate(s.clientPath(client.Name), client, 0o600)
}

func (s *Store) readRevocationsUnlocked() ([]Revocation, error) {
	var revocations []Revocation
	if err := readJSONStrict(s.revocationsPath, &revocations, 64<<10); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Revocation{}, nil
		}
		return nil, err
	}
	return revocations, nil
}

func (s *Store) listUnlocked() ([]Client, error) {
	entries, err := os.ReadDir(s.clientsDir)
	if err != nil {
		return nil, fmt.Errorf("read clients: %w", err)
	}
	clients := make([]Client, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var client Client
		if err := readJSONStrict(filepath.Join(s.clientsDir, entry.Name()), &client, 16<<10); err != nil {
			return nil, err
		}
		clients = append(clients, client)
	}
	sort.Slice(clients, func(i, j int) bool {
		return clients[i].Name < clients[j].Name
	})
	return clients, nil
}

func (s *Store) readClientUnlocked(name string) (Client, error) {
	var client Client
	if err := readJSONStrict(s.clientPath(name), &client, 16<<10); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Client{}, ErrNotFound
		}
		return Client{}, err
	}
	return client, nil
}

func (s *Store) withLock(fn func() error) error {
	f, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open state lock: %w", err)
	}
	defer f.Close()
	if err := lockExclusive(f); err != nil {
		return fmt.Errorf("lock state: %w", err)
	}
	defer unlock(f)
	return fn()
}

func (s *Store) clientPath(name string) string {
	return filepath.Join(s.clientsDir, name+".json")
}

func (s *Store) pendingPath(name string) string {
	return filepath.Join(s.pendingDir, name+".json")
}

func validateNewClient(client Client) error {
	if !invite.ValidName(client.Name) {
		return errors.New("client name is invalid")
	}
	if client.SerialHex == "" || client.CreatedAt.IsZero() || client.NotAfter.IsZero() {
		return errors.New("client metadata is incomplete")
	}
	return nil
}

func writeJSONCreate(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %q: %w", path, err)
	}
	data = append(data, '\n')
	return atomicfile.Create(path, data, mode)
}

func writeJSONReplace(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %q: %w", path, err)
	}
	data = append(data, '\n')
	return atomicfile.Replace(path, data, mode)
}

func readJSONStrict(path string, target any, limit int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return fmt.Errorf("read %q: %w", path, err)
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("state file %q exceeds size limit", path)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("decode %q: %w", path, err)
	}
	var extra any
	err = dec.Decode(&extra)
	if !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode %q: trailing JSON value", path)
		}
		return fmt.Errorf("decode %q: %w", path, err)
	}
	return nil
}

func syncDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory %q: %w", path, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync directory %q: %w", path, err)
	}
	return nil
}
