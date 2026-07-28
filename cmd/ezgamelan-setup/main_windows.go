//go:build windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/r3t2/ezgamelan/internal/setup"
	"golang.org/x/sys/windows"
)

const (
	messageOK            = 0x00000000
	messageOKCancel      = 0x00000001
	messageIconError     = 0x00000010
	messageIconInfo      = 0x00000040
	messageSetForeground = 0x00010000
	resultOK             = 1
)

func main() {
	confirmed, err := confirmInstall()
	if err != nil {
		showMessage("EZGameLAN 安装失败", err.Error(), messageOK|messageIconError)
		os.Exit(1)
	}
	if !confirmed {
		return
	}
	if err := install(); err != nil {
		showMessage(
			"EZGameLAN 安装失败",
			"安装过程出现问题：\n\n"+shortError(err),
			messageOK|messageIconError,
		)
		os.Exit(1)
	}
	showMessage(
		"EZGameLAN",
		"安装完成。EZGameLAN 客户端已经启动。",
		messageOK|messageIconInfo,
	)
}

func confirmInstall() (bool, error) {
	result, err := showMessage(
		"EZGameLAN 安装",
		"将安装 EZGameLAN、OpenVPN 核心和 TAP-Windows6 驱动。\n\n点击“确定”继续。",
		messageOKCancel|messageIconInfo,
	)
	if err != nil {
		return false, err
	}
	return result == resultOK, nil
}

func install() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位安装程序: %w", err)
	}
	temporary, err := os.MkdirTemp("", "EZGameLAN-Setup-*")
	if err != nil {
		return fmt.Errorf("创建临时目录: %w", err)
	}
	defer os.RemoveAll(temporary)
	if err := setup.Extract(executable, temporary); err != nil {
		return fmt.Errorf("校验安装包: %w", err)
	}
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		return fmt.Errorf("SystemRoot 环境变量为空")
	}
	powerShell := filepath.Join(
		systemRoot,
		"System32",
		"WindowsPowerShell",
		"v1.0",
		"powershell.exe",
	)
	if info, err := os.Lstat(powerShell); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("Windows PowerShell 路径异常")
	}
	command := exec.Command(
		powerShell,
		"-NoLogo",
		"-NoProfile",
		"-ExecutionPolicy", "Bypass",
		"-File", filepath.Join(temporary, "install.ps1"),
		"-Quiet",
	)
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	var output boundedOutput
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(output.String())
		if detail == "" {
			return fmt.Errorf("安装进程退出: %w", err)
		}
		return fmt.Errorf("安装进程退出: %w: %s", err, detail)
	}
	clientPath := filepath.Join(
		os.Getenv("ProgramFiles"),
		"EZGameLAN",
		"ezgamelan-client.exe",
	)
	if !filepath.IsAbs(clientPath) {
		return fmt.Errorf("ProgramFiles 环境变量为空")
	}
	client := exec.Command(clientPath)
	if err := client.Start(); err != nil {
		return fmt.Errorf("启动 EZGameLAN 客户端: %w", err)
	}
	_ = client.Process.Release()
	return nil
}

func showMessage(title, message string, kind uint32) (int32, error) {
	titlePointer, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return 0, err
	}
	messagePointer, err := windows.UTF16PtrFromString(message)
	if err != nil {
		return 0, err
	}
	return windows.MessageBox(
		0,
		messagePointer,
		titlePointer,
		kind|messageSetForeground,
	)
}

func shortError(err error) string {
	message := strings.ReplaceAll(err.Error(), "\x00", "")
	runes := []rune(message)
	const limit = 750
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return message
}

type boundedOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	const limit = 16 << 10
	b.mu.Lock()
	defer b.mu.Unlock()
	if remaining := limit - b.buffer.Len(); remaining > 0 {
		if len(data) > remaining {
			_, _ = b.buffer.Write(data[:remaining])
		} else {
			_, _ = b.buffer.Write(data)
		}
	}
	return len(data), nil
}

func (b *boundedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
