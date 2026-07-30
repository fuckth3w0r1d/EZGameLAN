//go:build windows

package adminweb

import (
	"fmt"
	"os/exec"
)

func OpenBrowser(url string) error {
	command := exec.Command(
		"rundll32.exe",
		"url.dll,FileProtocolHandler",
		url,
	)
	configureHiddenWindow(command)
	if err := command.Start(); err != nil {
		return fmt.Errorf("打开系统浏览器: %w", err)
	}
	return nil
}
