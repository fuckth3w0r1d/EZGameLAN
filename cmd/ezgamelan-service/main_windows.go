//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/r3t2/ezgamelan/internal/client"
	"github.com/r3t2/ezgamelan/internal/ipc"
	"github.com/r3t2/ezgamelan/internal/join"
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
	manager, err := client.New(client.Options{
		OpenVPNPath: openVPNPath,
		ConfigPath:  configPath,
	})
	if err != nil {
		return true, 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	pipe := ipc.NewPipeServer()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- pipe.Serve(ctx, manager.Handle)
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
					exitCode = 1
				}
				shutdownCancel()
				changes <- svc.Status{State: svc.Stopped}
				return exitCode != 0, exitCode
			}
		case err := <-serveErr:
			if err != nil && !errors.Is(err, context.Canceled) {
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
