//go:build linux

package openvpn_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/openvpn"
)

func TestLiveSessionsUsesManagementSocket(t *testing.T) {
	t.Parallel()
	socketPath := filepath.Join(t.TempDir(), "management.sock")
	command := startManagementFixture(t, socketPath, strings.Join([]string{
		">INFO:OpenVPN Management Interface Version 5",
		"TITLE\tOpenVPN 2.6.21",
		"CLIENT_LIST\talice\t198.51.100.2:1234\t10.86.0.10\t\t12\t34\t2026-07-28 11:00:00\t1785226800\tUNDEF\t5\t0\tAES-256-GCM",
		"END",
	}, "\n")+"\n")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sessions, err := openvpn.LiveSessions(ctx, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := <-command; got != "status 3" {
		t.Fatalf("command = %q", got)
	}
	if len(sessions) != 1 || sessions[0].Name != "alice" ||
		sessions[0].ClientID != 5 {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestDisconnectClientUsesNumericID(t *testing.T) {
	t.Parallel()
	socketPath := filepath.Join(t.TempDir(), "management.sock")
	command := startManagementFixture(
		t,
		socketPath,
		">INFO:OpenVPN Management Interface Version 5\n"+
			"SUCCESS: client-kill command succeeded\n",
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := openvpn.DisconnectClient(ctx, socketPath, 42); err != nil {
		t.Fatal(err)
	}
	if got := <-command; got != "client-kill 42" {
		t.Fatalf("command = %q", got)
	}
}

func startManagementFixture(
	t *testing.T,
	socketPath, response string,
) <-chan string {
	t.Helper()
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	command := make(chan string, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			command <- fmt.Sprintf("accept error: %v", err)
			return
		}
		defer connection.Close()
		if _, err := connection.Write([]byte(strings.SplitN(response, "\n", 2)[0] + "\n")); err != nil {
			command <- fmt.Sprintf("greeting error: %v", err)
			return
		}
		line, err := bufio.NewReader(connection).ReadString('\n')
		if err != nil {
			command <- fmt.Sprintf("read error: %v", err)
			return
		}
		command <- strings.TrimSpace(line)
		parts := strings.SplitN(response, "\n", 2)
		if len(parts) == 2 {
			_, _ = connection.Write([]byte(parts[1]))
		}
	}()
	return command
}
