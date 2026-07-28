package join

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/invite"
	"github.com/r3t2/ezgamelan/internal/pki"
)

func TestInstallCreatesPrivateConfigAndBackup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	output := filepath.Join(root, "OpenVPN", "config", "EZGameLAN", activeConfigName)
	profile := testProfile(t, time.Now(), "alice")
	result, err := Install(context.Background(), profile, output)
	if err != nil {
		t.Fatalf("first Install: %v", err)
	}
	if result.BackupPath != "" {
		t.Fatalf("first backup = %q", result.BackupPath)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "<key>") {
		t.Fatal("installed file does not contain the inline key")
	}

	old := []byte("old configuration\n")
	if err := os.WriteFile(output, old, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = Install(context.Background(), profile, output)
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	backup, err := os.ReadFile(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != string(old) {
		t.Fatalf("backup = %q", backup)
	}
	if info, err := os.Stat(output); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o", info.Mode().Perm())
	}
	if info, err := os.Stat(filepath.Dir(output)); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %o", info.Mode().Perm())
	}
}

func TestInstallRejectsExistingSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "OpenVPN", "config", "EZGameLAN", activeConfigName)
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, output); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), testProfile(t, time.Now(), "alice"), output); err == nil {
		t.Fatal("Install accepted an existing symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatal("symlink target changed")
	}
}

func testProfile(t *testing.T, now time.Time, name string) invite.Profile {
	t.Helper()
	pkiDir := filepath.Join(t.TempDir(), "pki")
	if err := pki.Init(pkiDir, "ezgamelan-server", now); err != nil {
		t.Fatal(err)
	}
	material, err := pki.IssueClient(pkiDir, name, now)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := pki.ReadCACertificate(pkiDir)
	if err != nil {
		t.Fatal(err)
	}
	return invite.Profile{
		FormatVersion: invite.FormatVersion,
		ClientName:    name,
		Endpoint:      "vpn.example.test:21111",
		ServerName:    "ezgamelan-server",
		CAPEM:         string(ca),
		CertPEM:       string(material.CertPEM),
		KeyPEM:        string(material.KeyPEM),
		TLSCryptV2PEM: "-----BEGIN OpenVPN tls-crypt-v2 client key-----\nTEST\n-----END OpenVPN tls-crypt-v2 client key-----\n",
	}
}
