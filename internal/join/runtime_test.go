package join

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/openvpn"
)

func TestRuntimeConfigUsesValidatedProfile(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	profile := testProfile(t, time.Now(), "alice")
	credential, err := store.Install(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	want, err := openvpn.ClientConfig(profile, 1400)
	if err != nil {
		t.Fatal(err)
	}
	runtimeDir := t.TempDir()
	path, err := store.RuntimeConfig(credential.ID, runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("runtime configuration mismatch: %v", err)
	}
	profile.KeyPEM += "</key>\nscript-security 2\nup evil.exe\n<key>"
	injected, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.profilePath(credential.ID), injected, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RuntimeConfig(credential.ID, runtimeDir); err == nil {
		t.Fatal("accepted injected key material")
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != want || strings.Contains(string(data), "evil.exe") {
		t.Fatalf("failed validation changed runtime configuration: %v", err)
	}
}
