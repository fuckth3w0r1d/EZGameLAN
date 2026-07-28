package adminweb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServerPreferenceRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "admin.json")
	data := []byte("{\n  \"format_version\": 1,\n  \"server\": \"user@vpn.example.test\"\n}\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadServerPreference(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "user@vpn.example.test" {
		t.Fatalf("server = %q", got)
	}
}

func TestServerPreferenceRejectsUnknownField(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "admin.json")
	if err := os.WriteFile(
		path,
		[]byte(`{"format_version":1,"server":"s0","token":"secret"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := loadServerPreference(path); err == nil {
		t.Fatal("expected an error")
	}
}
