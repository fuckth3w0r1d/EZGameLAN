//go:build linux

package openvpn

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const managementResponseLimit = 1 << 20

// LiveSessions queries the root-only OpenVPN management Unix socket.
func LiveSessions(ctx context.Context, socketPath string) ([]Session, error) {
	response, err := managementCommand(ctx, socketPath, "status 3")
	if err != nil {
		return nil, err
	}
	return ParseStatusVersion3(strings.NewReader(response))
}

// DisconnectClient closes one OpenVPN client connection by its transient ID.
func DisconnectClient(ctx context.Context, socketPath string, clientID uint64) error {
	response, err := managementCommand(
		ctx,
		socketPath,
		"client-kill "+strconv.FormatUint(clientID, 10),
	)
	if err != nil {
		return err
	}
	if !strings.Contains(response, "SUCCESS:") {
		return errors.New("OpenVPN did not confirm client disconnect")
	}
	return nil
}

func managementCommand(ctx context.Context, socketPath, command string) (string, error) {
	if socketPath == "" || !strings.HasPrefix(socketPath, "/") {
		return "", errors.New("management socket path must be absolute")
	}
	if strings.ContainsAny(command, "\r\n") {
		return "", errors.New("management command contains a line break")
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return "", fmt.Errorf("connect OpenVPN management socket: %w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return "", fmt.Errorf("set OpenVPN management deadline: %w", err)
	}
	if _, err := io.WriteString(conn, command+"\n"); err != nil {
		return "", fmt.Errorf("write OpenVPN management command: %w", err)
	}

	reader := bufio.NewReader(io.LimitReader(conn, managementResponseLimit+1))
	var result strings.Builder
	for {
		line, readErr := reader.ReadString('\n')
		if result.Len()+len(line) > managementResponseLimit {
			return "", errors.New("OpenVPN management response exceeds size limit")
		}
		result.WriteString(line)
		trimmed := strings.TrimSpace(line)
		if trimmed == "END" || strings.HasPrefix(trimmed, "SUCCESS:") ||
			strings.HasPrefix(trimmed, "ERROR:") {
			break
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return "", fmt.Errorf("read OpenVPN management response: %w", readErr)
		}
	}
	if strings.Contains(result.String(), "ERROR:") {
		return "", fmt.Errorf("OpenVPN management command failed: %s",
			strings.TrimSpace(result.String()))
	}
	return result.String(), nil
}

// ReadStatusFile is used as a fallback when the management socket is starting.
func ReadStatusFile(path string) ([]Session, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open OpenVPN status: %w", err)
	}
	defer file.Close()
	return ParseStatusVersion3(file)
}
