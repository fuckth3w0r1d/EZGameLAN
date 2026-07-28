//go:build windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const PipeName = `\\.\pipe\EZGameLAN-v2`

type Handler func(context.Context, Request) Response

type PipeServer struct {
	mu          sync.Mutex
	listener    windows.Handle
	connections map[*os.File]struct{}
	closed      bool
	wg          sync.WaitGroup
}

func NewPipeServer() *PipeServer {
	return &PipeServer{
		listener:    windows.InvalidHandle,
		connections: make(map[*os.File]struct{}),
	}
}

func (s *PipeServer) Serve(ctx context.Context, handler Handler) error {
	if handler == nil {
		return errors.New("IPC handler is nil")
	}
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-stop:
		}
	}()
	defer close(stop)

	slots := make(chan struct{}, 4)
	for {
		handle, err := s.accept()
		if err != nil {
			if ctx.Err() != nil || s.isClosed() {
				s.wg.Wait()
				return nil
			}
			s.Close()
			s.wg.Wait()
			return fmt.Errorf("accept named pipe: %w", err)
		}
		file := os.NewFile(uintptr(handle), PipeName)
		if file == nil {
			_ = windows.CloseHandle(handle)
			continue
		}
		s.track(file, true)
		slots <- struct{}{}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-slots }()
			defer s.track(file, false)
			defer file.Close()
			request, err := ReadRequest(file)
			if err != nil {
				_ = WriteResponse(file, Response{
					Version: Version,
					OK:      false,
					Code:    "E_SERVICE_PROTOCOL",
					Message: "客户端请求格式错误",
					Status:  Status{State: StateError, ErrorCode: "E_SERVICE_PROTOCOL"},
				})
				return
			}
			requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			_ = WriteResponse(file, handler(requestCtx, request))
		}()
	}
}

func (s *PipeServer) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.listener != windows.InvalidHandle {
		_ = windows.CloseHandle(s.listener)
		s.listener = windows.InvalidHandle
	}
	for connection := range s.connections {
		_ = connection.Close()
	}
	s.mu.Unlock()
}

func Call(ctx context.Context, request Request) (Response, error) {
	if err := request.Validate(); err != nil {
		return Response{}, err
	}
	dialCtx, cancelDial := context.WithTimeout(ctx, 3*time.Second)
	defer cancelDial()
	name, err := windows.UTF16PtrFromString(PipeName)
	if err != nil {
		return Response{}, err
	}
	var handle windows.Handle
	for {
		handle, err = windows.CreateFile(
			name,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0,
			nil,
			windows.OPEN_EXISTING,
			windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION,
			0,
		)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) &&
			!errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return Response{}, fmt.Errorf("connect named pipe: %w", err)
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-dialCtx.Done():
			return Response{}, fmt.Errorf("connect named pipe: %w", dialCtx.Err())
		}
	}
	file := os.NewFile(uintptr(handle), PipeName)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return Response{}, errors.New("open named pipe file failed")
	}
	defer file.Close()
	callDone := make(chan struct{})
	defer close(callDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = file.Close()
		case <-callDone:
		}
	}()
	if err := WriteRequest(file, request); err != nil {
		return Response{}, err
	}
	return ReadResponse(file)
}

func (s *PipeServer) accept() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(PipeName)
	if err != nil {
		return windows.InvalidHandle, err
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)",
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	handle, err := windows.CreateNamedPipe(
		name,
		windows.PIPE_ACCESS_DUPLEX,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|
			windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		4,
		MaxResponseSize,
		MaxRequestSize,
		5000,
		attributes,
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, os.ErrClosed
	}
	s.listener = handle
	s.mu.Unlock()

	err = windows.ConnectNamedPipe(handle, nil)
	s.mu.Lock()
	if s.listener == handle {
		s.listener = windows.InvalidHandle
	}
	s.mu.Unlock()
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, err
	}
	return handle, nil
}

func (s *PipeServer) track(file *os.File, add bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if add {
		s.connections[file] = struct{}{}
		return
	}
	delete(s.connections, file)
}

func (s *PipeServer) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
