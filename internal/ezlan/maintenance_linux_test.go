package ezlan

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/config"
	"github.com/r3t2/ezgamelan/internal/pki"
	"github.com/r3t2/ezgamelan/internal/store"
)

func TestMaintenanceRenewsAndRestoresMissingCRL(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	cfg := config.Default("vpn.example.test")
	cfg.PKIDir = filepath.Join(root, "pki")
	cfg.StateDir = filepath.Join(root, "state")
	if err := pki.Init(cfg.PKIDir, cfg.ServerName, now); err != nil {
		t.Fatal(err)
	}
	state, err := store.Open(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: cfg, store: state, now: func() time.Time { return now }}
	old := now.AddDate(-1, 0, 10)
	if err := pki.WriteCRL(cfg.PKIDir, []pki.Revocation{{SerialHex: "aa", At: old}}, old); err != nil {
		t.Fatal(err)
	}
	if err := m.Maintain(); err != nil {
		t.Fatal(err)
	}
	crl, err := pki.ReadCRL(cfg.PKIDir)
	if err != nil || crl == nil || !crl.NextUpdate.After(now.Add(300*24*time.Hour)) {
		t.Fatalf("renewal: %v, %v", crl, err)
	}
	if err := os.Remove(filepath.Join(cfg.PKIDir, pki.CRLFile)); err != nil {
		t.Fatal(err)
	}
	if err := m.Maintain(); err != nil {
		t.Fatal(err)
	}
	crl, err = pki.ReadCRL(cfg.PKIDir)
	if err != nil || len(crl.RevokedCertificateEntries) != 1 {
		t.Fatalf("lost CRL entries: %v, %v", crl, err)
	}
}

func TestEndpointUpdatePreservesConfiguration(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default("old.example.test")
	path := filepath.Join(root, "config.json")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := UpdateEndpoints(path, "new.example.test", "https://join.example.test/ezgamelan/v1/enroll"); err != nil {
		t.Fatal(err)
	}
	updated, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.PublicHost = updated.PublicHost
	cfg.EnrollURL = updated.EnrollURL
	if cfg != updated {
		t.Fatal("unrelated configuration changed")
	}
	if err := UpdateEndpoints(path, "bad host", ""); err == nil {
		t.Fatal("invalid update accepted")
	}
}
