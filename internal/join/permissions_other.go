//go:build !windows

package join

import (
	"context"
	"os"
)

func securePath(_ context.Context, path string, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}
