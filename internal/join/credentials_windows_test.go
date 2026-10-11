package join

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestPortableCredentialsAllowOnlyOwnerSystemAndAdministrators(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	store, err := NewCredentialStore(filepath.Join(t.TempDir(), "profiles"), sid)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.PrepareJoin(ctx); err != nil {
		t.Fatal(err)
	}
	credential, err := store.Install(ctx, testProfile(t, time.Now(), "alice"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{store.profilesDir, store.profilePath(credential.ID)} {
		descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		acl := descriptor.String()
		if !strings.Contains(acl, ";;;"+sid+")") || !strings.Contains(acl, ";;;SY)") || !strings.Contains(acl, ";;;BA)") {
			t.Fatalf("unexpected portable credential ACL: %s", acl)
		}
		if path == store.profilesDir && !strings.HasPrefix(acl, "D:P") {
			t.Fatalf("credential directory permits inherited access: %s", acl)
		}
		for _, ace := range strings.Split(acl, "(")[1:] {
			if !strings.HasSuffix(ace, ";;;"+sid+")") && !strings.HasSuffix(ace, ";;;SY)") && !strings.HasSuffix(ace, ";;;BA)") {
				t.Fatalf("unexpected credential trustee: %s", ace)
			}
		}
	}
	if err := store.Delete(credential.ID); err != nil {
		t.Fatal(err)
	}
}
