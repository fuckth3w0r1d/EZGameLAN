package ezlan

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/r3t2/ezgamelan/internal/config"
)

func TestEnsureExactFileReplacesGeneratedContent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "server.conf")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureExactFile(path, []byte("new\n"), 0o600); err != nil {
		t.Fatalf("ensureExactFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new\n" {
		t.Fatalf("contents = %q", data)
	}
}

func TestBuildJoinLinkKeepsTokenInFragment(t *testing.T) {
	t.Parallel()
	enrollURL := "https://vpn.example.test/ezgamelan/v1/enroll"
	token := "secret-token"
	raw := buildJoinLink(enrollURL, token)
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "ezgamelan" || parsed.Host != "join" {
		t.Fatalf("link target = %s://%s", parsed.Scheme, parsed.Host)
	}
	if got := parsed.Query().Get("url"); got != enrollURL {
		t.Fatalf("url = %q, want %q", got, enrollURL)
	}
	if parsed.Fragment != token {
		t.Fatalf("fragment = %q", parsed.Fragment)
	}
	if parsed.Query().Get("token") != "" {
		t.Fatal("token appeared in query")
	}
}

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
