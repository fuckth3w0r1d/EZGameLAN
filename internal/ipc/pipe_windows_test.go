package ipc

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestPipePartialRequestTimesOutAndServerRecovers(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	s := NewPipeServer(fmt.Sprintf(`\\.\pipe\EZGameLAN-test-%d`, time.Now().UnixNano()), user.User.Sid.String(), uint32(os.Getpid()))
	s.timeout = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- s.Serve(ctx, func(context.Context, Request) Response {
			return Response{Version: Version, OK: true, Status: Status{State: StateDisconnected}}
		})
	}()
	defer s.Close()
	name, _ := windows.UTF16PtrFromString(s.name)
	var handle windows.Handle
	for {
		var err error
		handle, err = windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
		if err == nil {
			break
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal(err)
		}
	}
	connection := &pipeConnection{handle: handle, ctx: ctx}
	if _, err := connection.Write([]byte{0, 0}); err != nil {
		windows.CloseHandle(handle)
		t.Fatal(err)
	}
	buffer := make([]byte, 1)
	if _, err := connection.Read(buffer); err == nil {
		t.Fatal("partial request did not time out")
	}
	windows.CloseHandle(handle)
	response, err := CallProcess(ctx, s.name, uint32(os.Getpid()), Request{Version: Version, Action: ActionStatus})
	if err != nil || !response.OK {
		t.Fatalf("server did not recover: %#v, %v", response, err)
	}
	s.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("server shutdown blocked")
	}
}

func TestPipeChecksBothProcessIdentities(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	pid := uint32(os.Getpid())
	for _, allowedPID := range []uint32{pid, pid + 1} {
		name := fmt.Sprintf(`\\.\pipe\EZGameLAN-identity-%d`, time.Now().UnixNano())
		server := NewPipeServer(name, user.User.Sid.String(), allowedPID)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		done := make(chan error, 1)
		go func() {
			done <- server.Serve(ctx, func(context.Context, Request) Response {
				return Response{Version: Version, OK: true, Status: Status{State: StateDisconnected}}
			})
		}()
		request := Request{Version: Version, Action: ActionStatus}
		if _, err := CallProcess(ctx, name, pid+1, request); err == nil {
			t.Error("accepted wrong server PID")
		}
		response, err := CallProcess(ctx, name, pid, request)
		if allowedPID == pid && (err != nil || !response.OK) {
			t.Errorf("valid identity failed: %v", err)
		}
		if allowedPID != pid && err == nil {
			t.Error("accepted wrong client PID")
		}
		server.Close()
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
