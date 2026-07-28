//go:build linux

package admin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/r3t2/ezgamelan/internal/logging"
	"github.com/r3t2/ezgamelan/internal/openvpn"
)

const (
	systemctlPath  = "/usr/bin/systemctl"
	journalctlPath = "/usr/bin/journalctl"
	hostOutputMax  = 64 << 10
)

type linuxHost struct{}

func newHost() host {
	return linuxHost{}
}

func (linuxHost) Sessions(ctx context.Context) ([]openvpn.Session, error) {
	sessions, socketErr := openvpn.LiveSessions(ctx, openvpn.ManagementSocketPath)
	if socketErr == nil {
		return sessions, nil
	}
	sessions, statusErr := openvpn.ReadStatusFile(openvpn.StatusPath)
	if statusErr == nil {
		return sessions, nil
	}
	return nil, errors.Join(socketErr, statusErr)
}

func (linuxHost) Disconnect(ctx context.Context, clientID uint64) error {
	return openvpn.DisconnectClient(ctx, openvpn.ManagementSocketPath, clientID)
}

func (linuxHost) ServiceState(ctx context.Context, unit string) (string, error) {
	if !validUnit(unit) {
		return "", errors.New("unsupported systemd unit")
	}
	output, err := runBounded(
		ctx,
		systemctlPath,
		"show",
		"--property=ActiveState",
		"--value",
		unit,
	)
	if err != nil {
		return "", err
	}
	state := strings.TrimSpace(output)
	switch state {
	case "active", "inactive", "failed", "activating", "deactivating", "reloading":
		return state, nil
	default:
		return "unknown", nil
	}
}

func (linuxHost) RestartService(ctx context.Context, unit string) error {
	if !validUnit(unit) {
		return errors.New("unsupported systemd unit")
	}
	_, err := runBounded(ctx, systemctlPath, "restart", unit)
	return err
}

func (linuxHost) Diagnostics(ctx context.Context) (string, error) {
	var result strings.Builder
	result.WriteString("EZGameLAN server diagnostics\n")
	for _, unit := range []string{OpenVPNUnit, EnrollUnit} {
		state, err := (linuxHost{}).ServiceState(ctx, unit)
		if err != nil {
			state = "unknown"
		}
		fmt.Fprintf(&result, "\n[%s] state=%s\n", unit, state)
		output, err := runBounded(
			ctx,
			journalctlPath,
			"--no-pager",
			"--quiet",
			"--lines=80",
			"--output=short-iso",
			"--unit",
			unit,
		)
		if err != nil {
			result.WriteString("journal unavailable\n")
			continue
		}
		result.WriteString(output)
		if !strings.HasSuffix(output, "\n") {
			result.WriteByte('\n')
		}
	}
	sanitized := logging.Sanitize([]byte(result.String()))
	if len(sanitized) > hostOutputMax {
		sanitized = sanitized[len(sanitized)-hostOutputMax:]
	}
	return strings.ToValidUTF8(sanitized, "\uFFFD"), nil
}

func validUnit(unit string) bool {
	return unit == OpenVPNUnit || unit == EnrollUnit
}

func runBounded(ctx context.Context, path string, args ...string) (string, error) {
	var output cappedBuffer
	output.limit = hostOutputMax
	command := exec.CommandContext(ctx, path, args...)
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("run %s: %w", path, err)
	}
	return output.String(), nil
}

type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		remaining := b.limit - b.Len()
		if remaining > 0 {
			_, _ = b.Buffer.Write(data[:remaining])
		}
		return len(data), errors.New("command output exceeds size limit")
	}
	return b.Buffer.Write(data)
}
