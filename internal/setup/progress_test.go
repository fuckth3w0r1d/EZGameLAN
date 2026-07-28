package setup_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/r3t2/ezgamelan/internal/setup"
)

func TestReadProgressReturnsLatestStage(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "progress.txt")
	if err := os.WriteFile(
		path,
		[]byte("elevation\nvalidate\nverify\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	progress, err := setup.ReadProgress(path)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Stage != "verify" || progress.Percent != 32 ||
		progress.Message == "" {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestReadProgressRejectsUnknownAndBackwardStages(t *testing.T) {
	t.Parallel()
	for name, data := range map[string]string{
		"unknown":  "validate\narbitrary\n",
		"backward": "verify\nvalidate\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "progress.txt")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := setup.ReadProgress(path); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
