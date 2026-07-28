package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/ipc"
)

func TestNewReportsConfigurationState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	options := Options{
		OpenVPNPath: filepath.Join(root, "openvpn"),
		ConfigPath:  filepath.Join(root, "ezgamelan.ovpn"),
	}
	manager, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.Status().State; got != ipc.StateNotConfigured {
		t.Fatalf("state = %q", got)
	}
	if err := os.WriteFile(options.ConfigPath, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.Status().State; got != ipc.StateDisconnected {
		t.Fatalf("state = %q", got)
	}
}

func TestHandleStatus(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manager, err := New(Options{
		OpenVPNPath: filepath.Join(root, "openvpn"),
		ConfigPath:  filepath.Join(root, "ezgamelan.ovpn"),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := manager.Handle(context.Background(), ipc.Request{
		Version: ipc.Version,
		Action:  ipc.ActionStatus,
	})
	if !response.OK || response.Status.State != ipc.StateNotConfigured {
		t.Fatalf("response = %#v", response)
	}
}

func TestBoundedLog(t *testing.T) {
	t.Parallel()
	log := &boundedLog{}
	data := []byte(strings.Repeat("x", 80<<10))
	n, err := log.Write(data)
	if err != nil || n != len(data) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if len(log.data) != 64<<10 {
		t.Fatalf("log size = %d", len(log.data))
	}
}

func TestRestartDelayIsBounded(t *testing.T) {
	t.Parallel()
	if got := restartDelay(0); got != time.Second {
		t.Fatalf("first delay = %s", got)
	}
	if got := restartDelay(10); got != 15*time.Second {
		t.Fatalf("bounded delay = %s", got)
	}
}

func TestErrorCodeDoesNotIncludeDetails(t *testing.T) {
	t.Parallel()
	err := errors.New("E_JOIN_TOKEN: secret detail")
	if got := errorCode(err); got != "E_JOIN_TOKEN" {
		t.Fatalf("code = %q", got)
	}
	if strings.Contains(userMessage(errorCode(err)), "secret") {
		t.Fatal("user message included internal details")
	}
}
