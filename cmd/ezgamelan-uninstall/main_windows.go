//go:build windows

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

const (
	messageOK             = 0x00000000
	messageOKCancel       = 0x00000001
	messageIconError      = 0x00000010
	messageIconWarning    = 0x00000030
	messageIconInfo       = 0x00000040
	messageDefaultButton2 = 0x00000100
	messageSetForeground  = 0x00010000
	resultOK              = 1
	workerArgument        = "--worker"
	workerWaitTimeoutMS   = 10_000
	maxExecutableSize     = 16 << 20
	cleanupScriptName     = "cleanup.ps1"
)

const cleanupScript = `param(
    [Parameter(Mandatory = $true)]
    [int]$ParentProcessId,
    [Parameter(Mandatory = $true)]
    [string]$TemporaryDirectory
)

Wait-Process -Id $ParentProcessId -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $TemporaryDirectory -Recurse -Force -ErrorAction SilentlyContinue
`

func main() {
	if len(os.Args) == 3 && os.Args[1] == workerArgument {
		parentPID, err := strconv.ParseUint(os.Args[2], 10, 32)
		if err != nil || parentPID == 0 {
			showMessage(
				"EZGameLAN 卸载失败",
				"卸载程序参数异常，请从 Windows 已安装的应用中重新启动卸载。",
				messageOK|messageIconError,
			)
			os.Exit(1)
		}
		if err := uninstall(uint32(parentPID)); err != nil {
			showMessage(
				"EZGameLAN 卸载失败",
				"卸载过程出现问题：\n\n"+shortError(err),
				messageOK|messageIconError,
			)
			os.Exit(1)
		}
		showMessage(
			"EZGameLAN",
			"卸载完成。",
			messageOK|messageIconInfo,
		)
		return
	}
	if len(os.Args) != 1 {
		showMessage(
			"EZGameLAN 卸载失败",
			"卸载程序参数异常，请从 Windows 已安装的应用中重新启动卸载。",
			messageOK|messageIconError,
		)
		os.Exit(1)
	}
	confirmed, err := confirmUninstall()
	if err != nil {
		showMessage("EZGameLAN 卸载失败", err.Error(), messageOK|messageIconError)
		os.Exit(1)
	}
	if !confirmed {
		return
	}
	if err := launchWorker(); err != nil {
		showMessage(
			"EZGameLAN 卸载失败",
			"启动卸载程序失败：\n\n"+shortError(err),
			messageOK|messageIconError,
		)
		os.Exit(1)
	}
}

func confirmUninstall() (bool, error) {
	result, err := showMessage(
		"卸载 EZGameLAN",
		"将卸载 EZGameLAN 并删除本机保存的局域网凭证。\n"+
			"由 EZGameLAN 安装的 OpenVPN/TAP 也会一并删除，用户原有安装会保留。\n\n"+
			"点击“确定”继续。",
		messageOKCancel|messageIconWarning|messageDefaultButton2,
	)
	if err != nil {
		return false, err
	}
	return result == resultOK, nil
}

func launchWorker() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位卸载程序: %w", err)
	}
	info, err := os.Lstat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 ||
		info.Size() > maxExecutableSize {
		return fmt.Errorf("卸载程序文件异常")
	}
	temporary, err := os.MkdirTemp("", "EZGameLAN-Uninstall-*")
	if err != nil {
		return fmt.Errorf("创建卸载临时目录: %w", err)
	}
	launched := false
	defer func() {
		if !launched {
			_ = os.RemoveAll(temporary)
		}
	}()
	worker := filepath.Join(temporary, "EZGameLAN-Uninstall.exe")
	if err := copyRegularFile(executable, worker, info.Size()); err != nil {
		return err
	}
	cleanupPath := filepath.Join(temporary, cleanupScriptName)
	if err := writeExclusive(cleanupPath, []byte(cleanupScript)); err != nil {
		return fmt.Errorf("准备卸载临时清理程序: %w", err)
	}
	command := exec.Command(worker, workerArgument, strconv.Itoa(os.Getpid()))
	command.SysProcAttr = hiddenProcessAttributes()
	if err := command.Start(); err != nil {
		return fmt.Errorf("启动卸载临时副本: %w", err)
	}
	launched = true
	_ = command.Process.Release()
	return nil
}

func uninstall(parentPID uint32) error {
	temporary, err := validateWorker()
	if err != nil {
		return err
	}
	powerShell, err := systemPowerShell()
	if err != nil {
		return err
	}
	defer startCleanup(powerShell, temporary)
	if err := waitForParent(parentPID); err != nil {
		return err
	}
	programFiles := os.Getenv("ProgramFiles")
	if programFiles == "" {
		return fmt.Errorf("ProgramFiles 环境变量为空")
	}
	script := filepath.Join(programFiles, "EZGameLAN", "uninstall.ps1")
	if info, err := os.Lstat(script); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("卸载脚本路径异常")
	}
	command := exec.Command(
		powerShell,
		"-NoLogo",
		"-NoProfile",
		"-ExecutionPolicy", "Bypass",
		"-File", script,
		"-Quiet",
	)
	command.SysProcAttr = hiddenProcessAttributes()
	var output boundedOutput
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(output.String())
		if detail == "" {
			return fmt.Errorf("卸载脚本退出: %w", err)
		}
		return fmt.Errorf("卸载脚本退出: %w: %s", err, detail)
	}
	return nil
}

func validateWorker() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("定位卸载临时副本: %w", err)
	}
	temporary := filepath.Clean(filepath.Dir(executable))
	tempRoot := filepath.Clean(os.TempDir())
	relative, err := filepath.Rel(tempRoot, temporary)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		!strings.HasPrefix(filepath.Base(temporary), "EZGameLAN-Uninstall-") {
		return "", fmt.Errorf("卸载临时副本路径异常")
	}
	for _, path := range []string{
		executable,
		filepath.Join(temporary, cleanupScriptName),
	} {
		if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("卸载临时文件异常")
		}
	}
	return temporary, nil
}

func waitForParent(parentPID uint32) error {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, parentPID)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return fmt.Errorf("等待卸载启动程序退出: %w", err)
	}
	defer windows.CloseHandle(process)
	event, err := windows.WaitForSingleObject(process, workerWaitTimeoutMS)
	if err != nil {
		return fmt.Errorf("等待卸载启动程序退出: %w", err)
	}
	if event == windows.WAIT_OBJECT_0 {
		return nil
	}
	if event == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("等待卸载启动程序退出超过 10 秒")
	}
	return fmt.Errorf("等待卸载启动程序退出时状态异常")
}

func systemPowerShell() (string, error) {
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		return "", fmt.Errorf("SystemRoot 环境变量为空")
	}
	path := filepath.Join(
		systemRoot,
		"System32",
		"WindowsPowerShell",
		"v1.0",
		"powershell.exe",
	)
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("Windows PowerShell 路径异常")
	}
	return path, nil
}

func startCleanup(powerShell, temporary string) {
	cleanupPath := filepath.Join(temporary, cleanupScriptName)
	command := exec.Command(
		powerShell,
		"-NoLogo",
		"-NoProfile",
		"-ExecutionPolicy", "Bypass",
		"-File", cleanupPath,
		"-ParentProcessId", strconv.Itoa(os.Getpid()),
		"-TemporaryDirectory", temporary,
	)
	command.Dir = os.TempDir()
	command.SysProcAttr = hiddenProcessAttributes()
	if err := command.Start(); err == nil {
		_ = command.Process.Release()
	}
}

func copyRegularFile(source, destination string, expectedSize int64) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("打开卸载程序: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return fmt.Errorf("创建卸载临时副本: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = output.Close()
		}
	}()
	written, err := io.Copy(output, io.LimitReader(input, maxExecutableSize+1))
	if err != nil {
		return fmt.Errorf("复制卸载程序: %w", err)
	}
	if written != expectedSize {
		return fmt.Errorf("卸载程序复制大小异常")
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("同步卸载临时副本: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("关闭卸载临时副本: %w", err)
	}
	closed = true
	return nil
}

func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	closed = true
	return nil
}

func hiddenProcessAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
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
