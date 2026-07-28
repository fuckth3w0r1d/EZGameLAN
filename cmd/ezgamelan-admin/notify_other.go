//go:build !windows

package main

import (
	"fmt"
	"os"
)

func notifyError(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
}
