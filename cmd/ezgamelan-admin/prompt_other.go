//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/r3t2/ezgamelan/internal/adminweb"
)

func promptServer(initial, problem string) (string, bool, error) {
	reader := bufio.NewReader(os.Stdin)
	for {
		if problem != "" {
			_, _ = fmt.Fprintln(os.Stderr, problem)
		}
		if initial != "" {
			_, _ = fmt.Fprintf(os.Stderr, "SSH server [%s]: ", initial)
		} else {
			_, _ = fmt.Fprint(os.Stderr, "SSH server alias or USER@HOST: ")
		}
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return "", false, err
		}
		target := strings.TrimSpace(line)
		if target == "" {
			target = initial
		}
		if target == "" {
			return "", false, nil
		}
		if err := adminweb.ValidateServerAlias(target); err != nil {
			problem = err.Error()
			continue
		}
		return target, true, nil
	}
}
