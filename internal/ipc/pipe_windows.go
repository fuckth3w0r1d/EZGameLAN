//go:build windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type Handler func(context.Context, Request) Response

type PipeServer struct {
	ownerSID  string
	clientPID uint32
	mu        sync.Mutex
	cancel    context.CancelFunc
	closed    bool
	name      string
	timeout   time.Duration
}

func NewPipeServer(name, ownerSID string, clientPID uint32) *PipeServer {
	return &PipeServer{name: name, ownerSID: ownerSID, clientPID: clientPID, timeout: 30 * time.Second}
}

func (s *PipeServer) Serve(parent context.Context, handler Handler) error {
	if handler == nil {
		return errors.New("IPC handler is nil")
	}
	if s.clientPID == 0 || s.ownerSID == "" {
		return errors.New("IPC client identity is missing")
	}
	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return nil
	}
	s.cancel = cancel
	s.mu.Unlock()
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	slots := make(chan struct{}, 4)
	for {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return nil
		}
		handle, err := s.accept(ctx)
		if err != nil {
			<-slots
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept named pipe: %w", err)
		}
		if s.clientPID != 0 {
			var pid uint32
			if err := windows.GetNamedPipeClientProcessId(handle, &pid); err != nil || pid != s.clientPID {
				windows.CloseHandle(handle)
				<-slots
				continue
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			defer windows.CloseHandle(handle)
			requestCtx, stop := context.WithTimeout(ctx, s.timeout)
			defer stop()
			connection := &pipeConnection{handle: handle, ctx: requestCtx}
			request, err := ReadRequest(connection)
			if err != nil {
				return
			}
			if requestCtx.Err() != nil {
				return
			}
			_ = WriteResponse(connection, handler(requestCtx, request))
		}()
	}
}

func (s *PipeServer) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
}

func CallProcess(parent context.Context, pipeName string, serverPID uint32, request Request) (Response, error) {
	if serverPID == 0 {
		return Response{}, errors.New("IPC server identity is missing")
	}
	if err := request.Validate(); err != nil {
		return Response{}, err
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	dialCtx, cancelDial := context.WithTimeout(ctx, 3*time.Second)
	defer cancelDial()
	name, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return Response{}, err
	}
	var handle windows.Handle
	for {
		if err := dialCtx.Err(); err != nil {
			return Response{}, err
		}
		handle, err = windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
			windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return Response{}, err
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-dialCtx.Done():
			return Response{}, dialCtx.Err()
		}
	}
	defer windows.CloseHandle(handle)
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(handle, &pid); err != nil || pid != serverPID {
		return Response{}, errors.New("IPC server identity mismatch")
	}
	connection := &pipeConnection{handle: handle, ctx: ctx}
	if err := WriteRequest(connection, request); err != nil {
		return Response{}, err
	}
	return ReadResponse(connection)
}

func (s *PipeServer) accept(ctx context.Context) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(s.name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	sid, err := windows.StringToSid(s.ownerSID)
	if err != nil {
		return windows.InvalidHandle, err
	}
	sddl := "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;" + sid.String() + ")"
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	handle, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		4, MaxResponseSize, MaxRequestSize, 0, attributes)
	if err != nil {
		return windows.InvalidHandle, err
	}
	_, err = pipeIO(ctx, handle, func(overlapped *windows.Overlapped, _ *uint32) error {
		err := windows.ConnectNamedPipe(handle, overlapped)
		if errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			return nil
		}
		return err
	})
	if err != nil {
		windows.CloseHandle(handle)
		return windows.InvalidHandle, err
	}
	return handle, nil
}

type pipeConnection struct {
	handle windows.Handle
	ctx    context.Context
}

func (p *pipeConnection) Read(data []byte) (int, error) {
	n, err := pipeIO(p.ctx, p.handle, func(o *windows.Overlapped, n *uint32) error { return windows.ReadFile(p.handle, data, n, o) })
	if errors.Is(err, windows.ERROR_BROKEN_PIPE) {
		err = io.EOF
	}
	return n, err
}
func (p *pipeConnection) Write(data []byte) (int, error) {
	return pipeIO(p.ctx, p.handle, func(o *windows.Overlapped, n *uint32) error { return windows.WriteFile(p.handle, data, n, o) })
}

// pipeIO waits for cancellation to complete before releasing OVERLAPPED memory.
func pipeIO(ctx context.Context, handle windows.Handle, start func(*windows.Overlapped, *uint32) error) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(event)
	overlapped := &windows.Overlapped{HEvent: event}
	var transferred uint32
	err = start(overlapped, &transferred)
	if !errors.Is(err, windows.ERROR_IO_PENDING) {
		return int(transferred), err
	}
	canceled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = windows.CancelIoEx(handle, overlapped)
		close(canceled)
	})
	err = windows.GetOverlappedResult(handle, overlapped, &transferred, true)
	if !stop() {
		<-canceled
	}
	if ctx.Err() != nil {
		return int(transferred), ctx.Err()
	}
	return int(transferred), err
}
