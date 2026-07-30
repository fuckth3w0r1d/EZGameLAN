//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/r3t2/ezgamelan/internal/client"
	"github.com/r3t2/ezgamelan/internal/ipc"
	"github.com/r3t2/ezgamelan/internal/join"
	"github.com/r3t2/ezgamelan/internal/logging"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/debug"
)

const serviceName = "EZGameLAN"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("E_SERVICE_MODE: 检查 Windows Service 环境: %w", err)
	}
	handler := &serviceHandler{}
	if isService {
		if err := svc.Run(serviceName, handler); err != nil {
			return fmt.Errorf("E_SERVICE_RUN: 运行 Windows Service: %w", err)
		}
		return nil
	}
	if err := debug.Run(serviceName, handler); err != nil {
		return fmt.Errorf("E_SERVICE_RUN: 调试运行 Windows Service: %w", err)
	}
	return nil
}

type serviceHandler struct{}

func (*serviceHandler) Execute(
	_ []string,
	requests <-chan svc.ChangeRequest,
	changes chan<- svc.Status,
) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}

	executable, err := os.Executable()
	if err != nil {
		return true, 1
	}
	configPath, err := join.DefaultOutputPath()
	if err != nil {
		return true, 1
	}
	openVPNPath, err := findOpenVPN(executable)
	if err != nil {
		return true, 1
	}
	dataDirectory := filepath.Dir(filepath.Dir(configPath))
	logFile, err := logging.Open(filepath.Join(dataDirectory, "logs", "service.log"))
	if err != nil {
		return true, 1
	}
	defer logFile.Close()
	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	logger.Info("EZGameLAN service starting")
	manager, err := client.New(client.Options{
		OpenVPNPath:       openVPNPath,
		LegacyConfigPath:  configPath,
		AdapterMarkerPath: filepath.Join(filepath.Dir(configPath), "adapter-original-name.txt"),
		Logger:            logger,
		ProcessLog:        logFile,
	})
	if err != nil {
		logger.Error("client manager initialization failed", "error", err)
		return true, 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	pipe := ipc.NewPipeServer()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- pipe.Serve(ctx, func(
			requestCtx context.Context,
			request ipc.Request,
		) ipc.Response {
			if request.Action == ipc.ActionDiagnostics {
				status, credentials := manager.Snapshot()
				return diagnosticsResponse(logFile, status, credentials)
			}
			return manager.Handle(requestCtx, request)
		})
	}()
	changes <- svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}

	exitCode := uint32(0)
	for {
		select {
		case request, ok := <-requests:
			if !ok {
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				pipe.Close()
				shutdownCtx, shutdownCancel := context.WithTimeout(
					context.Background(),
					15*time.Second,
				)
				if err := manager.Shutdown(shutdownCtx); err != nil {
					logger.Error("service shutdown failed", "error", err)
					exitCode = 1
				}
				shutdownCancel()
				changes <- svc.Status{State: svc.Stopped}
				return exitCode != 0, exitCode
			}
			switch request.Cmd {
			case svc.Interrogate:
				changes <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				pipe.Close()
				shutdownCtx, shutdownCancel := context.WithTimeout(
					context.Background(),
					15*time.Second,
				)
				if err := manager.Shutdown(shutdownCtx); err != nil {
					logger.Error("service shutdown failed", "error", err)
					exitCode = 1
				}
				shutdownCancel()
				changes <- svc.Status{State: svc.Stopped}
				return exitCode != 0, exitCode
			}
		case err := <-serveErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("named pipe server stopped", "error", err)
				exitCode = 1
			}
			cancel()
			shutdownCtx, shutdownCancel := context.WithTimeout(
				context.Background(),
				15*time.Second,
			)
			_ = manager.Shutdown(shutdownCtx)
			shutdownCancel()
			changes <- svc.Status{State: svc.Stopped}
			return exitCode != 0, exitCode
		}
	}
}

func diagnosticsResponse(
	logFile *logging.RotatingFile,
	status ipc.Status,
	credentials []ipc.Credential,
) ipc.Response {
	header := fmt.Sprintf(
		"EZGameLAN diagnostics\r\n"+
			"generated_at=%s\r\n"+
			"platform=%s/%s\r\n"+
			"state=%s\r\n"+
			"saved_credentials=%d\r\n"+
			"virtual_ip=%s\r\n"+
			"adapter=%s\r\n"+
			"\r\n=== recent bounded service log ===\r\n",
		time.Now().Format(time.RFC3339),
		runtime.GOOS,
		runtime.GOARCH,
		status.State,
		len(credentials),
		status.VirtualIP,
		status.AdapterName,
	)
	recent, err := logFile.Snapshot(ipc.MaxDiagnostics - len(header))
	if err != nil {
		return ipc.Response{
			Version:     ipc.Version,
			OK:          false,
			Code:        "E_INTERNAL_LOG",
			Message:     "读取诊断日志失败，请稍后重试",
			Status:      status,
			Credentials: credentials,
		}
	}
	return ipc.Response{
		Version:     ipc.Version,
		OK:          true,
		Status:      status,
		Credentials: credentials,
		Diagnostics: header + recent,
	}
}

func findOpenVPN(serviceExecutable string) (string, error) {
	candidates := []string{
		filepath.Join(filepath.Dir(serviceExecutable), "openvpn.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "OpenVPN", "bin", "openvpn.exe"),
	}
	for _, candidate := range candidates {
		if !filepath.IsAbs(candidate) {
			continue
		}
		info, err := os.Lstat(candidate)
		if err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", errors.New("E_INSTALL_OPENVPN: 未找到 OpenVPN 核心，请重新安装 EZGameLAN")
}
