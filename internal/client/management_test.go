package client

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/ipc"
)

func TestManagementStateTracksReconnectAndIgnoresOldProcess(t *testing.T) {
	m := &Manager{generation: 2, process: &exec.Cmd{}, status: ipc.Status{CredentialID: "saved"}}
	state, ip, ok := parseManagementState(">STATE:123,CONNECTED,SUCCESS,10.86.0.10,1.2.3.4")
	if !ok {
		t.Fatal("connected event rejected")
	}
	m.applyManagementState(2, state, ip, "EZGameLAN")
	if m.status.State != ipc.StateConnected || m.status.VirtualIP != ip {
		t.Fatal(m.status)
	}
	state, ip, ok = parseManagementState(">STATE:124,RECONNECTING,ping-restart,,")
	if !ok {
		t.Fatal("reconnect event rejected")
	}
	m.applyManagementState(2, state, ip, "")
	if m.status.State != ipc.StateConnecting || m.status.VirtualIP != "" {
		t.Fatal(m.status)
	}
	m.applyManagementState(1, ipc.StateConnected, "10.86.0.11", "old")
	if m.status.State != ipc.StateConnecting {
		t.Fatal("stale process changed state")
	}
	m.stopWanted = true
	m.applyManagementState(2, ipc.StateConnected, "10.86.0.10", "EZGameLAN")
	if m.status.State != ipc.StateConnecting {
		t.Fatal("stop was overridden")
	}
}

func TestQueuedOperationHonorsCancellation(t *testing.T) {
	m := &Manager{operation: make(chan struct{}, 1)}
	if err := m.acquireOperation(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.releaseOperation()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := m.acquireOperation(ctx); err == nil {
		t.Fatal("expired operation acquired lock")
	}
}
