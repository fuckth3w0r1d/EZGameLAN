//go:build !windows

package adminweb

import "os/exec"

func configureHiddenWindow(*exec.Cmd) {}
