package join

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCredentialStoreInstallsListsAndDeletesProfiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacyPath := filepath.Join(root, activeConfigName)
	store, err := NewCredentialStore(legacyPath)
	if err != nil {
		t.Fatal(err)
	}

	alice := testProfile(t, time.Now(), "alice")
	first, err := store.Install(context.Background(), alice)
	if err != nil {
		t.Fatalf("Install alice: %v", err)
	}
	if !validCredentialID(first.ID) || first.ClientName != "alice" {
		t.Fatalf("credential = %#v", first)
	}

	bob := testProfile(t, time.Now(), "bob")
	bob.Endpoint = "vpn-two.example.test:21111"
	second, err := store.Install(context.Background(), bob)
	if err != nil {
		t.Fatalf("Install bob: %v", err)
	}
	credentials, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(credentials) != 2 || !containsCredential(credentials, first) ||
		!containsCredential(credentials, second) {
		t.Fatalf("credentials = %#v", credentials)
	}

	configPath, err := store.ConfigPath(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(configPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o", info.Mode().Perm())
	}
	if err := store.Delete(first.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, path := range []string{
		configPath,
		configPath + ".bak",
		store.profileMetadataPath(first.ID),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("path %q remains: %v", path, err)
		}
	}
	credentials, err = store.List()
	if err != nil || len(credentials) != 1 || credentials[0] != second {
		t.Fatalf("credentials after delete = %#v, %v", credentials, err)
	}
}

func containsCredential(credentials []Credential, want Credential) bool {
	for _, credential := range credentials {
		if credential == want {
			return true
		}
	}
	return false
}

func TestCredentialStoreReplacesSameIdentityWithBackup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := NewCredentialStore(filepath.Join(root, activeConfigName))
	if err != nil {
		t.Fatal(err)
	}
	profile := testProfile(t, time.Now(), "alice")
	credential, err := store.Install(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	configPath, err := store.ConfigPath(credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	credentialAgain, err := store.Install(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	if credentialAgain != credential {
		t.Fatalf("replacement credential = %#v, want %#v", credentialAgain, credential)
	}
	backup, err := os.ReadFile(configPath + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != string(oldConfig) {
		t.Fatal("backup did not preserve the previous private configuration")
	}
	credentials, err := store.List()
	if err != nil || len(credentials) != 1 {
		t.Fatalf("credentials = %#v, %v", credentials, err)
	}
}

func TestCredentialStoreKeepsLegacyConfigVisible(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacyPath := filepath.Join(root, activeConfigName)
	if err := os.WriteFile(legacyPath, []byte("legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath+".bak", []byte("legacy backup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewCredentialStore(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 1 || credentials[0].ID != LegacyCredentialID ||
		!credentials[0].Legacy {
		t.Fatalf("legacy credentials = %#v", credentials)
	}
	if err := store.Delete(LegacyCredentialID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{legacyPath, legacyPath + ".bak"} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy path remains: %s", path)
		}
	}
}

func TestCredentialStoreDeletePreflightsAndResumesPartialDelete(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := NewCredentialStore(filepath.Join(root, activeConfigName))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.Install(
		context.Background(),
		testProfile(t, time.Now(), "alice"),
	)
	if err != nil {
		t.Fatal(err)
	}
	configPath := store.profileConfigPath(credential.ID)
	metadataPath := store.profileMetadataPath(credential.ID)
	backupPath := configPath + ".bak"
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(credential.ID); err == nil {
		t.Fatal("Delete accepted a backup symlink")
	}
	for _, path := range []string{configPath, metadataPath} {
		if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("preflight changed %q: %v", path, err)
		}
	}
	if err := os.Remove(backupPath); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(metadataPath); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(credential.ID); err != nil {
		t.Fatalf("resume Delete: %v", err)
	}
	if _, err := os.Lstat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configuration remains after resumed delete: %v", err)
	}
}

func TestCredentialStoreRejectsInvalidMetadataAndSymlinks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := NewCredentialStore(filepath.Join(root, activeConfigName))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.Install(
		context.Background(),
		testProfile(t, time.Now(), "alice"),
	)
	if err != nil {
		t.Fatal(err)
	}
	metadataPath := store.profileMetadataPath(credential.ID)
	metadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := strings.Replace(
		string(metadata),
		`"format_version":1`,
		`"format_version":1,"format_version":1`,
		1,
	)
	if err := os.WriteFile(metadataPath, []byte(duplicate), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("List accepted duplicate metadata fields")
	}

	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(root, "legacy.ovpn")
	if err := os.Symlink(target, legacyPath); err != nil {
		t.Fatal(err)
	}
	legacyStore, err := NewCredentialStore(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyStore.List(); err == nil {
		t.Fatal("List accepted a legacy configuration symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "keep" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
}
