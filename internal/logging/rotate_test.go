package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingFileBoundsRetentionAndSnapshot(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "service.log")
	logFile, err := openWithSecurity(path, 1024, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	for index := 0; index < 8; index++ {
		line := strings.Repeat(string(rune('a'+index)), 400) + "\n"
		if _, err := logFile.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	for _, candidate := range []string{path, path + ".1", path + ".2"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatalf("stat %s: %v", candidate, err)
		}
		if info.Size() > 1024 {
			t.Fatalf("%s size = %d", candidate, info.Size())
		}
	}
	snapshot, err := logFile.Snapshot(1500)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) > 1500 {
		t.Fatalf("snapshot size = %d", len(snapshot))
	}
	if !strings.Contains(snapshot, strings.Repeat("h", 100)) {
		t.Fatal("snapshot did not contain newest log data")
	}
}

func TestSanitizeRemovesCredentialsAndKeys(t *testing.T) {
	t.Parallel()
	input := `token="abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
ezgamelan://join?url=https%3A%2F%2Fvpn.example.test#abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG
<key>
private material
</key>
kept line
`
	got := Sanitize([]byte(input))
	if strings.Contains(got, "abcdefghijklmnopqrstuvwxyz") ||
		strings.Contains(got, "private material") {
		t.Fatalf("Sanitize leaked a secret: %q", got)
	}
	if !strings.Contains(got, "kept line") {
		t.Fatalf("Sanitize removed ordinary log text: %q", got)
	}
}
