//go:build windows

package client

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"

	"golang.org/x/sys/windows"
)

type windowsStopEvent struct {
	handle windows.Handle
}

func newOpenVPNCommand(executable, configPath string) (*exec.Cmd, processStopSignal, error) {
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
	return command, &windowsStopEvent{handle: handle}, nil
}

func (e *windowsStopEvent) Stop() error {
	return windows.SetEvent(e.handle)
}

func (e *windowsStopEvent) Close() {
	if e.handle != windows.InvalidHandle {
		_ = windows.CloseHandle(e.handle)
		e.handle = windows.InvalidHandle
	}
}
