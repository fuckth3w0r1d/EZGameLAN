package testutil

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func RequireAdminPermissions(t *testing.T) {
	t.Helper()
	if os.Getenv("EZGAMELAN_PRIVILEGED_TESTS") != "1" {
		t.Skip("set EZGAMELAN_PRIVILEGED_TESTS=1 in an elevated terminal for administrator ACL tests")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("administrator ACL tests require an elevated process")
	}
}

func CheckPrivatePath(t *testing.T, path string, _ os.FileMode) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	sddl := sd.String()
	if !strings.HasPrefix(sddl, "D:P") || !strings.Contains(sddl, ";;;SY)") || !strings.Contains(sddl, ";;;BA)") {
		t.Fatalf("unexpected administrator ACL: %s", sddl)
	}
	for _, ace := range strings.Split(sddl, "(")[1:] {
		if !strings.HasSuffix(ace, ";;;SY)") && !strings.HasSuffix(ace, ";;;BA)") {
			t.Fatalf("unexpected private path trustee: %s", ace)
		}
	}
}

func Symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if windows.GetCurrentProcessToken().IsElevated() {
			t.Fatal(err)
		}
		t.Skipf("symbolic-link test requires Windows developer mode or elevation: %v", err)
	}
}

func OwnerSID(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid.String()
}
