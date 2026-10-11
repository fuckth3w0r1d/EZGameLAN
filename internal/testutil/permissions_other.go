//go:build !windows

package testutil

import (
	"os"
	"testing"
)

func RequireAdminPermissions(t *testing.T) { t.Helper() }
func CheckPrivatePath(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Fatalf("%s: mode %o, want %o", path, info.Mode().Perm(), mode)
	}
}
func Symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func OwnerSID(t *testing.T) string { t.Helper(); return "test-owner" }
