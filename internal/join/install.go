package join

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/r3t2/ezgamelan/internal/atomicfile"
	"github.com/r3t2/ezgamelan/internal/invite"
	"github.com/r3t2/ezgamelan/internal/openvpn"
)

const (
	activeConfigName = "ezgamelan.ovpn"
	maxConfigSize    = 512 << 10
)

type InstallResult struct {
	Path       string
	BackupPath string
}

func DefaultOutputPath() (string, error) {
	return defaultOutputPath()
}

func Install(ctx context.Context, profile invite.Profile, outputPath string) (InstallResult, error) {
	if outputPath == "" {
		var err error
		outputPath, err = DefaultOutputPath()
		if err != nil {
			return InstallResult{}, err
		}
	}
	absolute, err := filepath.Abs(outputPath)
	if err != nil {
		return InstallResult{}, fmt.Errorf("E_INSTALL_PATH: 解析配置路径: %w", err)
	}
	data, err := openvpn.ClientConfig(profile, 1400)
	if err != nil {
		return InstallResult{}, fmt.Errorf("E_JOIN_CONFIG: 生成 OpenVPN 配置: %w", err)
	}
	if len(data) > maxConfigSize {
		return InstallResult{}, errors.New("E_JOIN_CONFIG: 生成的 OpenVPN 配置过大")
	}

	dir := filepath.Dir(absolute)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return InstallResult{}, fmt.Errorf("E_INSTALL_PATH: 创建配置目录: %w", err)
	}
	if err := securePath(ctx, dir, true); err != nil {
		return InstallResult{}, fmt.Errorf("E_INSTALL_PERMISSIONS: 保护配置目录: %w", err)
	}

	var previous []byte
	hadPrevious := false
	if info, err := os.Lstat(absolute); err == nil {
		if !info.Mode().IsRegular() {
			return InstallResult{}, errors.New("E_INSTALL_PATH: 现有配置不是普通文件")
		}
		previous, err = readBounded(absolute, maxConfigSize)
		if err != nil {
			return InstallResult{}, fmt.Errorf("E_INSTALL_PATH: 读取现有配置: %w", err)
		}
		hadPrevious = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return InstallResult{}, fmt.Errorf("E_INSTALL_PATH: 检查现有配置: %w", err)
	}

	result := InstallResult{Path: absolute}
	if hadPrevious {
		result.BackupPath = absolute + ".bak"
		if err := atomicfile.Replace(result.BackupPath, previous, 0o600); err != nil {
			return InstallResult{}, fmt.Errorf("E_INSTALL_BACKUP: 保存旧配置: %w", err)
		}
		if err := securePath(ctx, result.BackupPath, false); err != nil {
			return InstallResult{}, fmt.Errorf("E_INSTALL_PERMISSIONS: 保护配置备份: %w", err)
		}
	}

	if err := atomicfile.Replace(absolute, []byte(data), 0o600); err != nil {
		return InstallResult{}, fmt.Errorf("E_INSTALL_WRITE: 保存 OpenVPN 配置: %w", err)
	}
	if err := securePath(ctx, absolute, false); err != nil {
		if hadPrevious {
			_ = atomicfile.Replace(absolute, previous, 0o600)
			_ = securePath(ctx, absolute, false)
		} else {
			_ = os.Remove(absolute)
		}
		return InstallResult{}, fmt.Errorf("E_INSTALL_PERMISSIONS: 保护 OpenVPN 配置: %w", err)
	}
	return result, nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds size limit")
	}
	return data, nil
}
