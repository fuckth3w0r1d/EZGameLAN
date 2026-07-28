//go:build !windows

package client

import (
	"os"
	"os/exec"
)

type interruptSignal struct {
	command *exec.Cmd
}

func newOpenVPNCommand(executable, configPath string) (*exec.Cmd, processStopSignal, error) {
	command := exec.Command(executable, "--config", configPath)
	return command, &interruptSignal{command: command}, nil
}

func (s *interruptSignal) Stop() error {
	if s.command.Process == nil {
		return nil
	}
	return s.command.Process.Signal(os.Interrupt)
}

func (*interruptSignal) Close() {}
