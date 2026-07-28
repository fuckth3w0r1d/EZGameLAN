//go:build !linux

package store

import (
	"errors"
	"os"
)

func lockExclusive(_ *os.File) error {
	return errors.New("state locking is available on the Linux server")
}

func unlock(_ *os.File) {}
