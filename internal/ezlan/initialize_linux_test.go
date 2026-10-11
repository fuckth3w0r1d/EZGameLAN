package ezlan

import (
	"context"
	"github.com/r3t2/ezgamelan/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestInitializeMigratesVersionOneWithBackup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	legacy := []byte("{\"format_version\":1,\"public_host\":\"old.example.test\"}\n")
	if err := os.WriteFile(configPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default("vpn.example.test")
	cfg.StateDir = filepath.Join(root, "state")
	cfg.PKIDir = filepath.Join(root, "pki")
	cfg.OpenVPNPath = "/test/openvpn"
	if err := os.MkdirAll(cfg.PKIDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(cfg.PKIDir, "tls-crypt-v2-server.key"),
		[]byte("test key\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	if err := Initialize(context.Background(), configPath, cfg); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	got, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load migrated config: %v", err)
	}
	if got != cfg {
		t.Fatalf("migrated config = %#v, want %#v", got, cfg)
	}
	backup, err := os.ReadFile(configPath + ".v1.bak")
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backup) != string(legacy) {
		t.Fatal("legacy backup content changed")
	}
}
