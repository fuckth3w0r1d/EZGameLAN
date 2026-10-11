package portable

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/r3t2/ezgamelan/internal/client"
	"github.com/r3t2/ezgamelan/internal/ipc"
	"github.com/r3t2/ezgamelan/internal/join"
	"github.com/r3t2/ezgamelan/internal/logging"
	"github.com/r3t2/ezgamelan/internal/netcfg"
	"golang.org/x/sys/windows"
)

func RunBackend(args []string, manifest string) error {
	fs := flag.NewFlagSet("EZGameLAN-Backend", flag.ContinueOnError)
	parentPID := fs.Uint("parent", 0, "client process ID")
	parentCreated := fs.Uint64("created", 0, "client creation time")
	pipeName := fs.String("pipe", "", "instance pipe")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *parentPID == 0 || *parentCreated == 0 || !validPipeName(*pipeName) {
		return errors.New("后台参数无效")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("后台需要管理员权限")
	}
	mutexName, _ := windows.UTF16PtrFromString(`Global\EZGameLAN-Backend`)
	mutex, err := windows.CreateMutex(nil, false, mutexName)
	if err != nil {
		if mutex != 0 {
			windows.CloseHandle(mutex)
		}
		return errors.New("已有 EZGameLAN 后台正在运行")
	}
	defer windows.CloseHandle(mutex)
	parent, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(*parentPID))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(parent)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(parent, &created, &exited, &kernel, &user); err != nil {
		return err
	}
	if uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) != *parentCreated {
		return errors.New("客户端身份已改变")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	root := filepath.Dir(executable)
	image := make([]uint16, 32768)
	length := uint32(len(image))
	if err := windows.QueryFullProcessImageName(parent, 0, &image[0], &length); err != nil {
		return err
	}
	if !strings.EqualFold(windows.UTF16ToString(image[:length]), filepath.Join(root, "EZGameLAN.exe")) {
		return errors.New("后台只能由同目录客户端启动")
	}
	var token windows.Token
	if err := windows.OpenProcessToken(parent, windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	owner, err := token.GetTokenUser()
	token.Close()
	if err != nil {
		return err
	}
	ownerSID := owner.User.Sid.String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			state, err := windows.WaitForSingleObject(parent, 250)
			if err != nil || state == windows.WAIT_OBJECT_0 {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}()
	rootLocks, err := lockDirectories(root)
	if err != nil {
		return err
	}
	defer closeHandles(rootLocks)
	data := filepath.Join(root, "data")
	var dataLocks []windows.Handle
	defer func() { closeHandles(dataLocks) }()
	for _, dir := range []string{data, filepath.Join(data, "config"), filepath.Join(data, "config", "profiles")} {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		handles, err := lockDirectories(dir)
		if err != nil {
			return err
		}
		dataLocks = append(dataLocks, handles...)
		if err := join.ProtectPath(ctx, dir, true, ownerSID); err != nil {
			return err
		}
	}
	runtimeDir, err := prepareRuntime(ctx, root, manifest)
	if err != nil {
		return err
	}
	defer os.RemoveAll(runtimeDir)
	runtimeLocks, err := lockDirectories(runtimeDir)
	if err != nil {
		return err
	}
	defer closeHandles(runtimeLocks)
	logFile, err := logging.Open(filepath.Join(runtimeDir, "logs", "backend.log"))
	if err != nil {
		return err
	}
	defer logFile.Close()
	logger := slog.New(slog.NewTextHandler(logFile, nil))
	manager, err := client.New(client.Options{
		OpenVPNPath: filepath.Join(runtimeDir, "bin", "openvpn.exe"),
		ProfilesDir: filepath.Join(data, "config", "profiles"),
		OwnerSID:    ownerSID, RuntimeDir: runtimeDir, Logger: logger, ProcessLog: logFile,
		EnsureAdapter: func(ctx context.Context) (string, error) {
			if err := ensureTAPDriver(ctx, runtimeDir); err != nil {
				return "", err
			}
			return netcfg.EnsureDedicatedAdapter(ctx, ownerSID, filepath.Join(runtimeDir, "bin", "tapctl.exe"))
		},
	})
	if err != nil {
		return err
	}
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if err := manager.Shutdown(shutdown); err != nil {
			logger.Error("backend shutdown failed", "error", err)
		}
	}()
	pipe := ipc.NewPipeServer(*pipeName, ownerSID, uint32(*parentPID))
	defer pipe.Close()
	logger.Info("portable backend started")
	return pipe.Serve(ctx, func(requestCtx context.Context, request ipc.Request) ipc.Response {
		if request.Action == ipc.ActionDiagnostics {
			status, credentials := manager.Snapshot()
			recent, err := logFile.Snapshot(ipc.MaxDiagnostics)
			if err != nil {
				return ipc.Response{Version: ipc.Version, OK: false, Code: "E_INTERNAL_LOG", Message: "读取后台日志失败", Status: status}
			}
			return ipc.Response{Version: ipc.Version, OK: true, Status: status, Credentials: credentials, Diagnostics: string(recent)}
		}
		return manager.Handle(requestCtx, request)
	})
}

func validPipeName(name string) bool {
	const prefix = `\\.\pipe\EZGameLAN-`
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+32 {
		return false
	}
	for _, c := range strings.TrimPrefix(name, prefix) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Directory handles prevent path replacement while elevated operations use them.
func lockDirectories(path string) ([]windows.Handle, error) {
	var handles []windows.Handle
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		name, err := windows.UTF16PtrFromString(current)
		if err != nil {
			closeHandles(handles)
			return nil, err
		}
		handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			closeHandles(handles)
			return nil, err
		}
		var info windows.ByHandleFileInformation
		err = windows.GetFileInformationByHandle(handle, &info)
		if err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			windows.CloseHandle(handle)
			closeHandles(handles)
			return nil, errors.New("便携目录不能包含链接或目录重定向")
		}
		handles = append(handles, handle)
	}
	return handles, nil
}

func closeHandles(handles []windows.Handle) {
	for i := len(handles) - 1; i >= 0; i-- {
		windows.CloseHandle(handles[i])
	}
}

func prepareRuntime(ctx context.Context, root, encoded string) (string, error) {
	files, err := decodeManifest(encoded)
	if err != nil {
		return "", err
	}
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	directory := filepath.Join(programData, "EZGameLAN-Runtime-"+hex.EncodeToString(random))
	descriptor, err := windows.SecurityDescriptorFromString("O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return "", err
	}
	attributes := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	name, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return "", err
	}
	if err := windows.CreateDirectory(name, attributes); err != nil {
		return "", err
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(directory)
		}
	}()
	if err := join.ProtectPath(ctx, directory, true, ""); err != nil {
		return "", err
	}
	for name, digest := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		data, err := verifiedBytes(filepath.Join(root, "openvpn", filepath.FromSlash(name)), digest)
		if err != nil {
			return "", err
		}
		destination := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return "", err
		}
		if err := join.ProtectPath(ctx, filepath.Dir(destination), true, ""); err != nil {
			return "", err
		}
		if err := os.WriteFile(destination, data, 0o600); err != nil {
			return "", err
		}
		if err := join.ProtectPath(ctx, destination, false, ""); err != nil {
			return "", err
		}
	}
	success = true
	return directory, nil
}

func ensureTAPDriver(ctx context.Context, directory string) error {
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, filepath.Join(systemDirectory, "pnputil.exe"), "/add-driver", filepath.Join(directory, "driver", "OemVista.inf"), "/install")
	command.SysProcAttr = &windows.SysProcAttr{HideWindow: true}
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			switch exit.ExitCode() {
			case 259:
				return nil
			case 3010:
				return fmt.Errorf("E_ADAPTER_REBOOT: TAP 驱动安装完成，需要重启 Windows：%w", err)
			}
		}
		return fmt.Errorf("安装 TAP 驱动失败：%w；%s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
