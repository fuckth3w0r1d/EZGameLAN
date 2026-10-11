package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/ipc"
)

func TestManagementAuthenticatesBeforeReleasingHold(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{ctx: ctx, generation: 1}
	finished := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(finished)
		m.monitorManagement(&managementSession{address: listener.Addr().String(), password: "test-password"}, 1, done)
	}()
	defer func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("management monitor did not stop")
		}
	}()
	listener.(*net.TCPListener).SetDeadline(time.Now().Add(2 * time.Second))
	connection, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		connection.Close()
	}()
	reader := bufio.NewReader(connection)
	readCommand := func(want string) {
		t.Helper()
		connection.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := reader.ReadString('\n')
		if err != nil || line != want+"\n" {
			t.Fatalf("command = %q, %v; want %q", line, err, want)
		}
	}
	expectWaiting := func() {
		t.Helper()
		connection.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		line, err := reader.ReadString('\n')
		var timeout net.Error
		if line != "" || !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("command sent before OpenVPN was ready: %q, %v", line, err)
		}
	}
	fmt.Fprint(connection, "ENTER PASSWORD:")
	readCommand("test-password")
	expectWaiting()
	fmt.Fprint(connection, "SUCCESS: password is correct\r\n>INFO:OpenVPN Management Interface\r\n")
	readCommand("state on")
	readCommand("state")
	expectWaiting()
	fmt.Fprint(connection, ">HOLD:Waiting for hold release:0\r\n")
	readCommand("hold release")
	close(done)
}

func TestAdapterFailurePreservesRebootRequirement(t *testing.T) {
	for _, code := range []string{"E_ADAPTER", "E_ADAPTER_REBOOT"} {
		t.Run(code, func(t *testing.T) {
			failure := errors.New("adapter preparation failed")
			if code == "E_ADAPTER_REBOOT" {
				failure = errors.New("E_ADAPTER_REBOOT: restart required")
			}
			m := &Manager{ctx: context.Background(), options: Options{
				EnsureAdapter: func(context.Context) (string, error) { return "", failure },
			}}
			session, err := m.prepareManagement("unused.ovpn")
			if session != nil || !errors.Is(err, failure) || errorCode(err) != code {
				t.Fatalf("adapter failure = %v, %v", session, err)
			}
		})
	}
}

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
