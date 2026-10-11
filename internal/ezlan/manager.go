// Package ezlan connects the small server-side building blocks.
package ezlan

import (
	"context"
	"encoding/hex"
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
	ExpiresAt time.Time
}

func Initialize(ctx context.Context, configPath string, requested config.Config) error {
	cfg := requested
	replaceLegacy := false
	if existing, err := config.Load(configPath); err == nil {
		if existing.PublicHost != requested.PublicHost {
			return fmt.Errorf(
				"existing public host %q differs from requested host %q",
				existing.PublicHost,
				requested.PublicHost,
			)
		}
		if existing.EnrollURL != requested.EnrollURL {
			return errors.New("existing enrollment URL differs; use config-update --enroll-url")
		}
		cfg = existing
	} else if versionErr := new(config.VersionError); errors.As(err, &versionErr) &&
		versionErr.Got == 1 {
		legacyData, readErr := os.ReadFile(configPath)
		if readErr != nil {
			return fmt.Errorf("read legacy configuration: %w", readErr)
		}
		backupPath := configPath + ".v1.bak"
		if writeErr := atomicfile.Create(backupPath, legacyData, 0o600); writeErr != nil &&
			!errors.Is(writeErr, os.ErrExist) {
			return fmt.Errorf("back up legacy configuration: %w", writeErr)
		}
		replaceLegacy = true
	} else if !errors.Is(rootCause(err), os.ErrNotExist) {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	state, err := store.Open(cfg.StateDir)
	if err != nil {
		return err
	}
	now := time.Now()
	if err := pki.Init(cfg.PKIDir, cfg.ServerName, now); err != nil {
		return err
	}
	manager := &Manager{cfg: cfg, store: state, now: time.Now}
	if err := manager.Maintain(); err != nil {
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
	if replaceLegacy {
		if err := config.Replace(configPath, cfg); err != nil {
			return err
		}
	} else if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
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
	name string,
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
	client := store.Client{
		Name:      name,
		SerialHex: material.SerialHex,
		CreatedAt: now,
		NotAfter:  material.NotAfter,
	}

	token, err := invite.NewToken()
	if err != nil {
		return InviteResult{}, fmt.Errorf("generate invite token: %w", err)
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
	return InviteResult{
		Link:      buildJoinLink(m.cfg.EnrollURL, token),
		ExpiresAt: expires,
	}, nil
}

func buildJoinLink(enrollURL, token string) string {
	link := url.URL{
		Scheme:   "ezgamelan",
		Host:     "join",
		RawQuery: url.Values{"url": {enrollURL}}.Encode(),
		Fragment: token,
	}
	return link.String()
}

func (m *Manager) Revoke(name string) error {
	if err := m.Maintain(); err != nil {
		return err
	}
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

// Maintain recovers interrupted revocations and renews CRLs before expiry.
func (m *Manager) Maintain() error {
	now := m.now()
	var published map[string]bool
	var expires time.Time
	return m.store.ReconcileRevocations(func() ([]store.Revocation, error) {
		crl, err := pki.ReadCRL(m.cfg.PKIDir)
		if err != nil {
			return nil, err
		}
		published = make(map[string]bool)
		if crl == nil {
			return nil, nil
		}
		expires = crl.NextUpdate
		records := make([]store.Revocation, 0, len(crl.RevokedCertificateEntries))
		for _, entry := range crl.RevokedCertificateEntries {
			serial := hex.EncodeToString(entry.SerialNumber.Bytes())
			published[serial] = true
			records = append(records, store.Revocation{SerialHex: serial, RevokedAt: entry.RevocationTime})
		}
		return records, nil
	}, func(records []store.Revocation) error {
		refresh := !expires.After(now.Add(30 * 24 * time.Hour))
		revoked := make([]pki.Revocation, 0, len(records))
		for _, record := range records {
			refresh = refresh || !published[record.SerialHex]
			revoked = append(revoked, pki.Revocation{SerialHex: record.SerialHex, At: record.RevokedAt})
		}
		if !refresh {
			return nil
		}
		return pki.WriteCRL(m.cfg.PKIDir, revoked, now)
	})
}

func (m *Manager) CRLWarning() string {
	crl, err := pki.ReadCRL(m.cfg.PKIDir)
	if err != nil || crl == nil {
		return "吊销列表不可用，请运行 ezlanctl maintain"
	}
	if !crl.NextUpdate.After(m.now().Add(30 * 24 * time.Hour)) {
		return "吊销列表即将或已经过期，请检查维护任务"
	}
	records, err := m.store.Revocations()
	if err != nil {
		return "吊销记录读取失败，请检查维护任务"
	}
	published := make(map[string]bool, len(crl.RevokedCertificateEntries))
	for _, entry := range crl.RevokedCertificateEntries {
		published[hex.EncodeToString(entry.SerialNumber.Bytes())] = true
	}
	for _, record := range records {
		if !published[record.SerialHex] {
			return "存在尚未生效的吊销记录，请重试吊销并检查维护任务"
		}
	}
	return ""
}

// UpdateEndpoints changes advertised addresses without replacing identities.
func UpdateEndpoints(path, host, enrollURL string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	if host != "" {
		cfg.PublicHost = host
	}
	if enrollURL != "" {
		cfg.EnrollURL = enrollURL
	}
	return config.Replace(path, cfg)
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
