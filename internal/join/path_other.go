//go:build !windows

package join

import (
	"fmt"
	"os"
	"path/filepath"
)

func defaultOutputPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("E_INSTALL_PATH: 获取用户目录: %w", err)
	}
	return filepath.Join(home, ".config", "ezgamelan", activeConfigName), nil
}
