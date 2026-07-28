package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := Default("vpn.example.test")
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.PKIDir = filepath.Join(dir, "pki")
	cfg.EnrollTLSDir = filepath.Join(dir, "enroll")
	cfg.OpenVPNPath = "/test/openvpn"
	path := filepath.Join(dir, "config.json")

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != cfg {
		t.Fatalf("Load = %#v, want %#v", got, cfg)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := Default("127.0.0.1")
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.PKIDir = filepath.Join(dir, "pki")
	cfg.EnrollTLSDir = filepath.Join(dir, "enroll")
	cfg.OpenVPNPath = "/test/openvpn"
	path := filepath.Join(dir, "config.json")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "\n}", ",\n  \"typo\": true\n}", 1))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted unknown field")
	}
}

func TestValidateRejectsNonCanonicalSubnet(t *testing.T) {
	t.Parallel()
	cfg := Default("127.0.0.1")
	cfg.VirtualCIDR = "10.86.0.1/24"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted non-canonical subnet")
	}
}
