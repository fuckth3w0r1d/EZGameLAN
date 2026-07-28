//go:build windows

package join

import (
	"errors"
	"os"
	"path/filepath"
)

func defaultOutputPath() (string, error) {
	programData := os.Getenv("ProgramData")
	if programData == "" {
		return "", errors.New("E_INSTALL_PATH: ProgramData 环境变量为空")
	}
	return filepath.Join(
		programData,
		"EZGameLAN",
		"config",
		activeConfigName,
	), nil
}
