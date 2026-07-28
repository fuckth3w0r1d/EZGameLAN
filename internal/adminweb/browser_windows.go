//go:build windows

package adminweb

import (
	"fmt"
	"os/exec"
)

func OpenBrowser(url string) error {
	if err := exec.Command(
		"rundll32.exe",
		"url.dll,FileProtocolHandler",
		url,
	).Start(); err != nil {
		return fmt.Errorf("打开系统浏览器: %w", err)
	}
	return nil
}
