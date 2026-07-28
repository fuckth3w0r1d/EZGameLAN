package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateAndReplace(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := Create(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := Create(path, []byte("second"), 0o600); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second Create error = %v, want os.ErrExist", err)
	}
	if err := Replace(path, []byte("second"), 0o600); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "second" {
		t.Fatalf("contents = %q, want second", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %#o, want 0600", got)
	}
}

func TestCreateRejectsSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Create(link, []byte("replacement"), 0o600); !errors.Is(err, os.ErrExist) {
		t.Fatalf("Create error = %v, want os.ErrExist", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "unchanged" {
		t.Fatalf("symlink target changed to %q", data)
	}
}
