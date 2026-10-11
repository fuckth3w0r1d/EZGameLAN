package portable

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/r3t2/ezgamelan/internal/ipc"
	"github.com/r3t2/ezgamelan/internal/join"
	"golang.org/x/sys/windows"
)

type Session struct {
	mu                   sync.Mutex
	root, pipe, ownerSID string
	backend              windows.Handle
	backendPID           uint32
	instance             windows.Handle
	closed               bool
}

func NewSession() (*Session, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	s := &Session{root: filepath.Dir(executable), ownerSID: user.User.Sid.String()}
	name, _ := windows.UTF16PtrFromString(`Local\EZGameLAN-Client-` + s.ownerSID)
	s.instance, err = windows.CreateMutex(nil, false, name)
	if err != nil {
		if s.instance != 0 {
			windows.CloseHandle(s.instance)
		}
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return nil, errors.New("EZGameLAN 已在运行，请使用现有窗口")
		}
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			windows.CloseHandle(s.instance)
		}
	}()
	data := filepath.Join(s.root, "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		return nil, err
	}
	if err := join.ProtectPath(context.Background(), data, true, s.ownerSID); err != nil {
		return nil, err
	}
	success = true
	return s, nil
}

func (s *Session) DataDirectory() string { return filepath.Join(s.root, "data") }

func (s *Session) Call(ctx context.Context, request ipc.Request) (ipc.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return ipc.Response{}, err
	}
	if err := request.Validate(); err != nil {
		return ipc.Response{}, err
	}
	if s.closed {
		return ipc.Response{}, errors.New("客户端已退出")
	}
	if s.backend != 0 {
		state, _ := windows.WaitForSingleObject(s.backend, 0)
		if state == windows.WAIT_OBJECT_0 {
			windows.CloseHandle(s.backend)
			s.backend = 0
			s.backendPID = 0
		}
	}
	if s.backend == 0 {
		if request.Action == ipc.ActionStatus || request.Action == ipc.ActionDiagnostics || request.Action == ipc.ActionDeleteCredential || request.Action == ipc.ActionDisconnect {
			return s.localResponse(request)
		}
		if err := s.startBackend(ctx); err != nil {
			return ipc.Response{}, err
		}
	}
	return ipc.CallProcess(ctx, s.pipe, s.backendPID, request)
}

func (s *Session) localResponse(request ipc.Request) (ipc.Response, error) {
	store, err := join.NewCredentialStore(filepath.Join(s.DataDirectory(), "config", "profiles"), s.ownerSID)
	if err != nil {
		return ipc.Response{}, err
	}
	if request.Action == ipc.ActionDeleteCredential {
		if err := store.Delete(request.CredentialID); err != nil {
			return ipc.Response{}, err
		}
	}
	credentials, err := store.List()
	if err != nil {
		return ipc.Response{}, err
	}
	response := ipc.Response{Version: ipc.Version, OK: true, Status: ipc.Status{State: ipc.StateNotConfigured}}
	for _, item := range credentials {
		response.Credentials = append(response.Credentials, ipc.Credential{ID: item.ID, ClientName: item.ClientName, Endpoint: item.Endpoint, ServerName: item.ServerName})
	}
	if len(credentials) > 0 {
		response.Status.State = ipc.StateDisconnected
	}
	if request.Action == ipc.ActionDiagnostics {
		response.Diagnostics = "后台当前未运行。"
	}
	return response, nil
}

type shellExecuteInfo struct {
	Size                              uint32
	Mask                              uint32
	Window                            windows.Handle
	Verb, File, Parameters, Directory *uint16
	Show                              int32
	Instance                          windows.Handle
	IDList                            uintptr
	Class                             *uint16
	ClassKey                          windows.Handle
	HotKey                            uint32
	Icon                              windows.Handle
	Process                           windows.Handle
}

func (s *Session) startBackend(ctx context.Context) error {
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	s.pipe = `\\.\pipe\EZGameLAN-` + hex.EncodeToString(secret)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
		return err
	}
	creation := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	arguments := "--parent " + strconv.Itoa(os.Getpid()) + " --created " + strconv.FormatUint(creation, 10) + " --pipe " + s.pipe
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(filepath.Join(s.root, "EZGameLAN-Backend.exe"))
	params, _ := windows.UTF16PtrFromString(arguments)
	directory, _ := windows.UTF16PtrFromString(s.root)
	info := shellExecuteInfo{Mask: 0x40 | 0x100 | 0x400, Verb: verb, File: file, Parameters: params, Directory: directory, Show: 0}
	info.Size = uint32(unsafe.Sizeof(info))
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, 2); err != nil && err != syscall.Errno(1) {
		return err
	}
	defer windows.CoUninitialize()
	result, _, err := windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW").Call(uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		return fmt.Errorf("启动后台需要管理员许可：%w", err)
	}
	if info.Process == 0 {
		return errors.New("无法取得后台进程")
	}
	s.backend = info.Process
	pid, err := windows.GetProcessId(info.Process)
	if err != nil {
		windows.CloseHandle(info.Process)
		s.backend = 0
		return err
	}
	s.backendPID = pid
	ready, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		_, err := ipc.CallProcess(ready, s.pipe, pid, ipc.Request{Version: ipc.Version, Action: ipc.ActionStatus})
		if err == nil {
			return nil
		}
		state, _ := windows.WaitForSingleObject(s.backend, 0)
		if state == windows.WAIT_OBJECT_0 {
			return errors.New("后台启动失败，请确认程序已完整解压、目录可写且没有其他连接实例")
		}
		if ready.Err() != nil {
			return fmt.Errorf("等待后台启动：%w", ready.Err())
		}
	}
}

func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.instance != 0 {
		windows.CloseHandle(s.instance)
		s.instance = 0
	}
	if s.backend == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	_, _ = ipc.CallProcess(ctx, s.pipe, s.backendPID, ipc.Request{Version: ipc.Version, Action: ipc.ActionDisconnect})
	cancel()
	windows.CloseHandle(s.backend)
	s.backend = 0
}
