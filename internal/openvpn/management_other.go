//go:build !linux

package openvpn

import (
	"context"
	"errors"
)

func LiveSessions(context.Context, string) ([]Session, error) {
	return nil, errors.New("OpenVPN management is only available on Linux")
}

func DisconnectClient(context.Context, string, uint64) error {
	return errors.New("OpenVPN management is only available on Linux")
}

func ReadStatusFile(string) ([]Session, error) {
	return nil, errors.New("OpenVPN status file is only available on Linux")
}
