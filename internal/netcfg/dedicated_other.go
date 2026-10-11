//go:build !windows

package netcfg

import "errors"

func AdapterAddress(_, _ string) (string, string, error) {
	return "", "", errors.New("TAP adapter inspection requires Windows")
}
