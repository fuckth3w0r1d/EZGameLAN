//go:build !linux

package admin

import (
	"context"
	"errors"

	"github.com/r3t2/ezgamelan/internal/openvpn"
)

type unsupportedHost struct{}

func newHost() host {
	return unsupportedHost{}
}

func (unsupportedHost) Sessions(context.Context) ([]openvpn.Session, error) {
	return nil, errors.New("server management is only available on Linux")
}

func (unsupportedHost) Disconnect(context.Context, uint64) error {
	return errors.New("server management is only available on Linux")
}

func (unsupportedHost) ServiceState(context.Context, string) (string, error) {
	return "", errors.New("server management is only available on Linux")
}

func (unsupportedHost) RestartService(context.Context, string) error {
	return errors.New("server management is only available on Linux")
}

func (unsupportedHost) Diagnostics(context.Context) (string, error) {
	return "", errors.New("server management is only available on Linux")
}
