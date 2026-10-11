//go:build windows

package client

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsStopEvent struct {
	handle windows.Handle
	job    windows.Handle
}

func (e *windowsStopEvent) Bind(command *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	e.job = job
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	return windows.AssignProcessToJobObject(job, process)
}

func newOpenVPNCommand(executable, configPath string, arguments ...string) (*exec.Cmd, processStopSignal, error) {
	systemRoot, err := windows.GetWindowsDirectory()
	if err != nil {
		return nil, nil, err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, nil, err
	}
	eventName := "EZGameLAN-" + hex.EncodeToString(random)
	name, err := windows.UTF16PtrFromString(eventName)
	if err != nil {
		return nil, nil, err
	}
	handle, err := windows.CreateEvent(nil, 1, 0, name)
	if err != nil {
		return nil, nil, fmt.Errorf("create OpenVPN stop event: %w", err)
	}
	command := exec.Command(
		executable,
		"--config", configPath,
		"--service", eventName, "0",
	)
	command.Args = append(command.Args, arguments...)
	command.Dir = filepath.Dir(executable)
	command.Env = []string{"SystemRoot=" + systemRoot, "WINDIR=" + systemRoot, "TEMP=" + filepath.Dir(configPath), "TMP=" + filepath.Dir(configPath)}
	command.SysProcAttr = &windows.SysProcAttr{HideWindow: true}
	return command, &windowsStopEvent{handle: handle}, nil
}

func (e *windowsStopEvent) Stop() error {
	return windows.SetEvent(e.handle)
}

func (e *windowsStopEvent) Close() {
	if e.job != 0 {
		_ = windows.CloseHandle(e.job)
		e.job = 0
	}
	if e.handle != windows.InvalidHandle {
		_ = windows.CloseHandle(e.handle)
		e.handle = windows.InvalidHandle
	}
}
