//go:build windows

package netcfg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

func renameAdapter(ctx context.Context, currentName, newName string) error {
	systemRoot := os.Getenv("SystemRoot")
	if !filepath.IsAbs(systemRoot) {
		return errors.New("E_ADAPTER_NAME: SystemRoot 路径无效")
	}
	netshPath := filepath.Join(systemRoot, "System32", "netsh.exe")
	if info, err := os.Lstat(netshPath); err != nil || !info.Mode().IsRegular() {
		return errors.New("E_ADAPTER_NAME: 未找到 Windows netsh")
	}
	command := exec.CommandContext(
		ctx,
		netshPath,
		"interface",
		"set",
		"interface",
		"name="+currentName,
		"newname="+newName,
	)
	output := &limitedOutput{}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(output.String())
		if detail == "" {
			return fmt.Errorf("E_ADAPTER_NAME: 重命名网卡: %w", err)
		}
		return fmt.Errorf("E_ADAPTER_NAME: 重命名网卡: %w: %s", err, detail)
	}
	return nil
}

type limitedOutput struct {
	mu   sync.Mutex
	data []byte
}

func (b *limitedOutput) Write(data []byte) (int, error) {
	const limit = 16 << 10
	original := len(data)
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(data) >= limit {
		b.data = append(b.data[:0], data[len(data)-limit:]...)
		return original, nil
	}
	if overflow := len(b.data) + len(data) - limit; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, data...)
	return original, nil
}

func (b *limitedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}
