package adminweb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/r3t2/ezgamelan/internal/atomicfile"
)

const preferenceVersion = 1

type preference struct {
	FormatVersion int    `json:"format_version"`
	Server        string `json:"server"`
}

// LoadServerPreference reads the last successfully connected SSH target.
func LoadServerPreference() (string, error) {
	path, err := preferencePath()
	if err != nil {
		return "", err
	}
	return loadServerPreference(path)
}

// SaveServerPreference stores only an SSH alias or USER@HOST, never keys or
// authentication material.
func SaveServerPreference(server string) error {
	if err := ValidateServerAlias(server); err != nil {
		return err
	}
	path, err := preferencePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(preference{
		FormatVersion: preferenceVersion,
		Server:        server,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("编码管理端设置: %w", err)
	}
	data = append(data, '\n')
	if err := atomicfile.Replace(path, data, 0o600); err != nil {
		return fmt.Errorf("保存管理端设置: %w", err)
	}
	return nil
}

func preferencePath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("定位当前用户配置目录: %w", err)
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("当前用户配置目录不是绝对路径")
	}
	return filepath.Join(root, "EZGameLAN", "admin.json"), nil
}

func loadServerPreference(path string) (string, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取管理端设置: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("检查管理端设置: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", errors.New("管理端设置文件格式异常")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return "", fmt.Errorf("读取管理端设置: %w", err)
	}
	var saved preference
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&saved); err != nil {
		return "", fmt.Errorf("解析管理端设置: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", errors.New("管理端设置包含多余数据")
	}
	if saved.FormatVersion != preferenceVersion {
		return "", errors.New("管理端设置版本异常")
	}
	if err := ValidateServerAlias(saved.Server); err != nil {
		return "", fmt.Errorf("管理端设置中的 SSH 目标: %w", err)
	}
	return saved.Server, nil
}
