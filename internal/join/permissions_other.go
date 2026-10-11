//go:build !windows

package join

import (
	"context"
	"os"
)

func validateOwnerSID(_ string) error { return nil }

func ProtectPath(ctx context.Context, path string, directory bool, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}
