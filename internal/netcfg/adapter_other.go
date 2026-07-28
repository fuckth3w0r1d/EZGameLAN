//go:build !windows

package netcfg

import "context"

func renameAdapter(_ context.Context, _, _ string) error {
	return nil
}
