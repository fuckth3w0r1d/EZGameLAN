//go:build windows

package adminweb

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureHiddenWindow(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
