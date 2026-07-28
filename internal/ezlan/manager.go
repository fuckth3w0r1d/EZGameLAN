// Package ezlan connects the small server-side building blocks.
package ezlan

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/r3t2/ezgamelan/internal/atomicfile"
	"github.com/r3t2/ezgamelan/internal/config"
	"github.com/r3t2/ezgamelan/internal/invite"
	"github.com/r3t2/ezgamelan/internal/openvpn"
	"github.com/r3t2/ezgamelan/internal/pki"
	"github.com/r3t2/ezgamelan/internal/store"
)

const inviteLifetime = 30 * time.Minute

type Manager struct {
	configPath string
	cfg        config.Config
	store      *store.Store
	now        func() time.Time
}

type InviteResult struct {
	Link      string
	File      string
	ExpiresAt time.Time
}

func Initialize(ctx context.Context, configPath string, requested config.Config) error {
	cfg := requested
	if existing, err := config.Load(configPath); err == nil {
		if existing.PublicHost != requested.PublicHost {
			return fmt.Errorf(
				"existing public host %q differs from requested host %q",
				existing.PublicHost,
				requested.PublicHost,
			)
		}
		cfg = existing
	} else if !errors.Is(rootCause(err), os.ErrNotExist) {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if _, err := store.Open(cfg.StateDir); err != nil {
		return err
	}
	now := time.Now()
	if err := pki.Init(cfg.PKIDir, cfg.EnrollTLSDir, cfg.PublicHost, cfg.ServerName, now); err != nil {
		return err
	}
	serverTLSKey := filepath.Join(cfg.PKIDir, "tls-crypt-v2-server.key")
	if err := openvpn.EnsureTLSCryptV2ServerKey(
		ctx,
		cfg.OpenVPNPath,
		serverTLSKey,
	); err != nil {
		return err
	}
	serverConfig, err := openvpn.ServerConfig(cfg)
	if err != nil {
		return err
	}
	serverConfigPath := filepath.Join(filepath.Dir(configPath), "server.conf")
	if err := ensureExactFile(serverConfigPath, []byte(serverConfig), 0o600); err != nil {
		return fmt.Errorf("write OpenVPN server config: %w", err)
	}
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		if err := config.Save(configPath, cfg); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("inspect configuration: %w", err)
	}
	return nil
}

func Open(configPath string) (*Manager, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	state, err := store.Open(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	return &Manager{
		configPath: configPath,
		cfg:        cfg,
		store:      state,
		now:        time.Now,
	}, nil
}

func (m *Manager) Config() config.Config {
	return m.cfg
}

func (m *Manager) State() *store.Store {
	return m.store
}

func (m *Manager) Invite(
	ctx context.Context,
	name, outputPath string,
) (InviteResult, error) {
	if !invite.ValidName(name) {
		return InviteResult{}, errors.New("client name must use 1..32 letters, digits, '-' or '_'")
	}
	now := m.now()
	material, err := pki.IssueClient(m.cfg.PKIDir, name, now)
	if err != nil {
		return InviteResult{}, err
	}
	tlsKey, err := openvpn.GenerateTLSCryptV2ClientKey(
		ctx,
		m.cfg.OpenVPNPath,
		filepath.Join(m.cfg.PKIDir, "tls-crypt-v2-server.key"),
	)
	if err != nil {
		return InviteResult{}, err
	}
	ca, err := pki.ReadCACertificate(m.cfg.PKIDir)
	if err != nil {
		return InviteResult{}, err
	}
	profile := invite.Profile{
		FormatVersion: invite.FormatVersion,
		ClientName:    name,
		Endpoint:      m.cfg.OpenVPNEndpoint(),
		ServerName:    m.cfg.ServerName,
		CAPEM:         string(ca),
		CertPEM:       string(material.CertPEM),
		KeyPEM:        string(material.KeyPEM),
		TLSCryptV2PEM: string(tlsKey),
	}
	profileData, err := invite.Marshal(profile, now)
	if err != nil {
		return InviteResult{}, err
	}
	client := store.Client{
		Name:      name,
		SerialHex: material.SerialHex,
		CreatedAt: now,
		NotAfter:  material.NotAfter,
	}

	if outputPath != "" {
		if !filepath.IsAbs(outputPath) {
			absolute, err := filepath.Abs(outputPath)
			if err != nil {
				return InviteResult{}, fmt.Errorf("resolve output path: %w", err)
			}
			outputPath = absolute
		}
		if err := atomicfile.Create(outputPath, profileData, 0o600); err != nil {
			return InviteResult{}, err
		}
		if err := m.store.AddActive(client, now); err != nil {
			_ = os.Remove(outputPath)
			return InviteResult{}, err
		}
		return InviteResult{File: outputPath}, nil
	}

	token, err := invite.NewToken()
	if err != nil {
		return InviteResult{}, fmt.Errorf("generate invite token: %w", err)
	}
	pin, err := pki.EnrollmentPin(m.cfg.EnrollTLSDir)
	if err != nil {
		return InviteResult{}, err
	}
	expires := now.Add(inviteLifetime)
	pending := store.Pending{
		FormatVersion: 1,
		Name:          name,
		TokenHash:     invite.HashToken(token),
		ExpiresAt:     expires,
		Profile:       profile,
	}
	if err := m.store.AddPending(client, pending, now); err != nil {
		return InviteResult{}, err
	}
	link := url.URL{
		Scheme:   "ezgamelan",
		Host:     "join",
		RawQuery: url.Values{"server": {m.cfg.EnrollEndpoint()}, "pin": {pin}}.Encode(),
		Fragment: token,
	}
	return InviteResult{Link: link.String(), ExpiresAt: expires}, nil
}

func (m *Manager) Revoke(name string) error {
	now := m.now()
	return m.store.Revoke(name, now, func(records []store.Revocation) error {
		revoked := make([]pki.Revocation, 0, len(records))
		for _, record := range records {
			revoked = append(revoked, pki.Revocation{
				SerialHex: record.SerialHex,
				At:        record.RevokedAt,
			})
		}
		return pki.WriteCRL(m.cfg.PKIDir, revoked, now)
	})
}

func (m *Manager) List() ([]store.Client, error) {
	return m.store.List(m.now())
}

func ensureExactFile(path string, data []byte, mode os.FileMode) error {
	existing, err := os.ReadFile(path)
	if err == nil {
		if strings.TrimSpace(string(existing)) != strings.TrimSpace(string(data)) {
			return atomicfile.Replace(path, data, mode)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicfile.Create(path, data, mode)
}

func rootCause(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}
