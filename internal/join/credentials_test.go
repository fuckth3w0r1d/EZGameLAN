package join

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/invite"
	"github.com/r3t2/ezgamelan/internal/pki"
	"github.com/r3t2/ezgamelan/internal/testutil"
)

func TestCredentialStoreInstallsListsAndDeletesProfiles(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	first, err := store.Install(context.Background(), testProfile(t, time.Now(), "alice"))
	if err != nil {
		t.Fatal(err)
	}
	bob := testProfile(t, time.Now(), "bob")
	bob.Endpoint = "vpn-two.example.test:21111"
	second, err := store.Install(context.Background(), bob)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := store.List()
	if err != nil || len(credentials) != 2 || credentials[0] != second || credentials[1] != first {
		t.Fatalf("credentials = %#v, %v", credentials, err)
	}
	if !ValidCredentialID(first.ID) || first.ClientName != "alice" {
		t.Fatalf("credential = %#v", first)
	}
	entries, err := os.ReadDir(store.profilesDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("stored files = %v, %v", entries, err)
	}
	if err := store.Delete(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(store.profilePath(first.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted profile remains: %v", err)
	}
	credentials, err = store.List()
	if err != nil || len(credentials) != 1 || credentials[0] != second {
		t.Fatalf("credentials after delete = %#v, %v", credentials, err)
	}
}

func TestCredentialStoreReplacesSameIdentity(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	profile := testProfile(t, time.Now(), "alice")
	credential, err := store.Install(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	replacement := testProfile(t, time.Now(), "alice")
	again, err := store.Install(context.Background(), replacement)
	if err != nil || again != credential {
		t.Fatalf("replacement = %#v, %v", again, err)
	}
	got, err := store.readProfile(credential.ID)
	if err != nil || got != replacement {
		t.Fatalf("replacement was not saved: %v", err)
	}
	entries, err := os.ReadDir(store.profilesDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != credential.ID+".json" {
		t.Fatalf("stored files = %v, %v", entries, err)
	}
	for _, scenario := range []string{"invalid profile", "cancelled save"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			candidate := profile
			if scenario == "invalid profile" {
				candidate.KeyPEM = "invalid"
			} else {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if _, err := store.Install(ctx, candidate); err == nil {
				t.Fatal("invalid save succeeded")
			}
			got, err := store.readProfile(credential.ID)
			if err != nil || got != replacement {
				t.Fatalf("failed save changed the credential: %v", err)
			}
		})
	}
}

func TestCredentialStoreRejectsInvalidProfiles(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	credential, err := store.Install(context.Background(), testProfile(t, time.Now(), "alice"))
	if err != nil {
		t.Fatal(err)
	}
	path := store.profilePath(credential.ID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct{ name, content string }{
		{"duplicate field", strings.Replace(string(data), "\"format_version\": 1", "\"format_version\": 1, \"format_version\": 1", 1)},
		{"unknown field", strings.Replace(string(data), "{", "{\"directives\": \"up evil.exe\",", 1)},
		{"identity mismatch", strings.Replace(string(data), "\"alice\"", "\"bob\"", 1)},
		{"oversized", strings.Repeat(" ", invite.MaxSize+1)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(scenario.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.List(); err == nil {
				t.Fatal("List accepted invalid profile")
			}
			if _, err := store.RuntimeConfig(credential.ID, t.TempDir()); err == nil {
				t.Fatal("RuntimeConfig accepted invalid profile")
			}
		})
	}
}

func TestCredentialStoreRejectsSymlinks(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	profile := testProfile(t, time.Now(), "alice")
	credential, err := store.Install(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	path := store.profilePath(credential.ID)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.Symlink(t, target, path)
	if _, err := store.List(); err == nil {
		t.Fatal("List accepted a symlink")
	}
	if _, err := store.Install(context.Background(), profile); err == nil {
		t.Fatal("Install accepted a symlink")
	}
	if err := store.Delete(credential.ID); err == nil {
		t.Fatal("Delete accepted a symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "keep" {
		t.Fatalf("symlink target changed: %v", err)
	}
}

func TestExpiredCredentialCanBeListedAndDeleted(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	profile := testProfile(t, time.Now().AddDate(-20, 0, 0), "alice")
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareJoin(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := credentialForProfile(profile).ID
	if err := os.WriteFile(store.profilePath(id), data, 0o600); err != nil {
		t.Fatal(err)
	}
	credentials, err := store.List()
	if err != nil || len(credentials) != 1 || credentials[0].ID != id {
		t.Fatalf("expired credential missing: %#v, %v", credentials, err)
	}
	if _, err := store.RuntimeConfig(id, t.TempDir()); err == nil {
		t.Fatal("expired credential accepted for connection")
	}
	if err := store.Delete(id); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialStoreRejectsInvalidIDs(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	for _, id := range []string{"", "legacy", "../outside", strings.Repeat("A", 32)} {
		if _, err := store.RuntimeConfig(id, t.TempDir()); err == nil {
			t.Fatalf("RuntimeConfig accepted %q", id)
		}
		if err := store.Delete(id); err == nil {
			t.Fatalf("Delete accepted %q", id)
		}
	}
}

func newTestStore(t *testing.T) *CredentialStore {
	t.Helper()
	store, err := NewCredentialStore(filepath.Join(t.TempDir(), "profiles"), testutil.OwnerSID(t))
	if err != nil {
		t.Fatal(err)
	}
	return store
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
