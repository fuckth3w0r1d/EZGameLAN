package ezlan

import (
	"os"
	"path/filepath"
	"testing"
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
