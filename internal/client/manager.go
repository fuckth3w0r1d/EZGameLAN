// Package client owns the single Windows-side OpenVPN process and its state.
package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/r3t2/ezgamelan/internal/ipc"
	"github.com/r3t2/ezgamelan/internal/join"
)

const maxRestartAttempts = 5

type Options struct {
	OpenVPNPath string
	ConfigPath  string
	HTTPClient  *http.Client
}

type processStopSignal interface {
	Stop() error
	Close()
}

type Manager struct {
	options Options

	operationMu sync.Mutex
	mu          sync.Mutex
	status      ipc.Status
	process     *exec.Cmd
	processStop processStopSignal
	processDone chan struct{}
	stopWanted  bool
	generation  uint64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func New(options Options) (*Manager, error) {
	if !filepath.IsAbs(options.OpenVPNPath) || !filepath.IsAbs(options.ConfigPath) {
		return nil, errors.New("E_INSTALL_PATH: OpenVPN 和配置路径必须是绝对路径")
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{
		options: options,
		status: ipc.Status{
			State: ipc.StateNotConfigured,
		},
		ctx:    ctx,
		cancel: cancel,
	}
	if info, err := os.Lstat(options.ConfigPath); err == nil && info.Mode().IsRegular() {
		manager.status.State = ipc.StateDisconnected
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		cancel()
		return nil, fmt.Errorf("E_INSTALL_PATH: 检查活动配置: %w", err)
	}
	return manager, nil
}

func (m *Manager) Handle(ctx context.Context, request ipc.Request) ipc.Response {
	var err error
	switch request.Action {
	case ipc.ActionJoin:
		err = m.Join(ctx, request.URL, request.Token)
	case ipc.ActionConnect:
		err = m.Connect()
	case ipc.ActionDisconnect:
		err = m.Disconnect(ctx)
	case ipc.ActionStatus:
	default:
		err = errors.New("E_SERVICE_REQUEST: 未知操作")
	}
	status := m.Status()
	if err != nil {
		code := errorCode(err)
		return ipc.Response{
			Version: ipc.Version,
			OK:      false,
			Code:    code,
			Message: userMessage(code),
			Status:  status,
		}
	}
	return ipc.Response{
		Version: ipc.Version,
		OK:      true,
		Status:  status,
	}
}

func (m *Manager) Join(ctx context.Context, enrollURL, token string) error {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()

	invitation, err := join.NewInvitation(enrollURL, token)
	if err != nil {
		return err
	}
	profile, err := join.Fetch(ctx, m.options.HTTPClient, invitation)
	if err != nil {
		return err
	}
	if err := m.stopProcess(ctx); err != nil {
		return err
	}
	if _, err := join.Install(ctx, profile, m.options.ConfigPath); err != nil {
		return err
	}
	return m.connectLocked()
}

func (m *Manager) Connect() error {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	return m.connectLocked()
}

func (m *Manager) connectLocked() error {
	m.mu.Lock()
	if m.process != nil {
		m.mu.Unlock()
		return nil
	}
	if _, err := os.Stat(m.options.ConfigPath); err != nil {
		m.mu.Unlock()
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("E_OPENVPN_CONFIG: 请先输入 URL 和 Token 加入")
		}
		return fmt.Errorf("E_OPENVPN_CONFIG: 检查活动配置: %w", err)
	}
	m.stopWanted = false
	command, stopSignal, done, generation, err := m.launchLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.wg.Add(1)
	go m.supervise(command, stopSignal, done, generation)
	return nil
}

func (m *Manager) Disconnect(ctx context.Context) error {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	return m.stopProcess(ctx)
}

func (m *Manager) stopProcess(ctx context.Context) error {
	m.mu.Lock()
	m.stopWanted = true
	command := m.process
	stopSignal := m.processStop
	done := m.processDone
	if command == nil {
		if m.status.State != ipc.StateNotConfigured {
			m.status = ipc.Status{State: ipc.StateDisconnected}
		}
		m.mu.Unlock()
		return nil
	}
	m.status = ipc.Status{State: ipc.StateStopping}
	m.mu.Unlock()

	if stopSignal != nil {
		if err := stopSignal.Stop(); err == nil {
			graceTimer := time.NewTimer(8 * time.Second)
			select {
			case <-done:
				if !graceTimer.Stop() {
					<-graceTimer.C
				}
				return nil
			case <-graceTimer.C:
			case <-ctx.Done():
				graceTimer.Stop()
				return fmt.Errorf("E_OPENVPN_STOP: 等待 OpenVPN 退出: %w", ctx.Err())
			}
		}
	}
	if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("E_OPENVPN_STOP: 终止 OpenVPN: %w", err)
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("E_OPENVPN_STOP: 等待 OpenVPN 退出: %w", ctx.Err())
	}
}

func (m *Manager) Status() ipc.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.cancel()
	err := m.Disconnect(ctx)
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return err
	case <-ctx.Done():
		if err != nil {
			return err
		}
		return fmt.Errorf("E_SERVICE_STOP: 等待后台任务: %w", ctx.Err())
	}
}

func (m *Manager) launchLocked() (*exec.Cmd, processStopSignal, chan struct{}, uint64, error) {
	command, stopSignal, err := newOpenVPNCommand(m.options.OpenVPNPath, m.options.ConfigPath)
	if err != nil {
		m.status = ipc.Status{State: ipc.StateError, ErrorCode: "E_OPENVPN_START"}
		return nil, nil, nil, 0, fmt.Errorf("E_OPENVPN_START: 准备 OpenVPN: %w", err)
	}
	command.Env = minimalEnvironment()
	command.Stdout = &boundedLog{}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		stopSignal.Close()
		m.status = ipc.Status{State: ipc.StateError, ErrorCode: "E_OPENVPN_START"}
		return nil, nil, nil, 0, fmt.Errorf("E_OPENVPN_START: 启动 OpenVPN: %w", err)
	}
	m.generation++
	generation := m.generation
	done := make(chan struct{})
	m.process = command
	m.processStop = stopSignal
	m.processDone = done
	m.status = ipc.Status{State: ipc.StateConnecting}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.monitorVirtualIP(generation, done)
	}()
	return command, stopSignal, done, generation, nil
}

func (m *Manager) supervise(
	command *exec.Cmd,
	stopSignal processStopSignal,
	done chan struct{},
	generation uint64,
) {
	defer m.wg.Done()
	current := command
	currentStop := stopSignal
	currentDone := done
	currentGeneration := generation
	restarts := 0
	for {
		_ = current.Wait()
		currentStop.Close()

		m.mu.Lock()
		if m.generation == currentGeneration {
			m.process = nil
			m.processStop = nil
			m.processDone = nil
		}
		stop := m.stopWanted || m.ctx.Err() != nil
		if stop {
			if m.status.State != ipc.StateNotConfigured {
				m.status = ipc.Status{State: ipc.StateDisconnected}
			}
			m.mu.Unlock()
			close(currentDone)
			return
		}
		m.status = ipc.Status{State: ipc.StateError, ErrorCode: "E_OPENVPN_EXIT"}
		m.mu.Unlock()
		close(currentDone)

		launched := false
		for restarts < maxRestartAttempts {
			delay := restartDelay(restarts)
			restarts++
			select {
			case <-time.After(delay):
			case <-m.ctx.Done():
				return
			}

			m.mu.Lock()
			if m.stopWanted || m.ctx.Err() != nil || m.process != nil {
				m.mu.Unlock()
				return
			}
			next, nextStop, nextDone, nextGeneration, err := m.launchLocked()
			m.mu.Unlock()
			if err != nil {
				continue
			}
			current = next
			currentStop = nextStop
			currentDone = nextDone
			currentGeneration = nextGeneration
			launched = true
			break
		}
		if !launched {
			return
		}
	}
}

func (m *Manager) monitorVirtualIP(generation uint64, done <-chan struct{}) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ip := findVirtualIP()
			if ip == "" {
				continue
			}
			m.mu.Lock()
			if m.generation == generation && m.process != nil {
				m.status = ipc.Status{State: ipc.StateConnected, VirtualIP: ip}
			}
			m.mu.Unlock()
			return
		case <-done:
			return
		case <-m.ctx.Done():
			return
		}
	}
}

func findVirtualIP() string {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, address := range addresses {
		var ip net.IP
		switch value := address.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		}
		ip4 := ip.To4()
		if ip4 != nil && ip4[0] == 10 && ip4[1] == 86 && ip4[2] == 0 &&
			ip4[3] >= 10 && ip4[3] <= 19 {
			return ip4.String()
		}
	}
	return ""
}

func restartDelay(attempt int) time.Duration {
	delay := time.Second << attempt
	if delay > 15*time.Second {
		return 15 * time.Second
	}
	return delay
}

func minimalEnvironment() []string {
	allowed := []string{"SystemRoot", "WINDIR", "TEMP", "TMP"}
	result := make([]string, 0, len(allowed))
	for _, name := range allowed {
		if value, ok := os.LookupEnv(name); ok {
			result = append(result, name+"="+value)
		}
	}
	return result
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if index := strings.IndexByte(text, ':'); index > 0 && index <= 64 {
		code := text[:index]
		if strings.HasPrefix(code, "E_") {
			return code
		}
	}
	return "E_INTERNAL_CLIENT"
}

func userMessage(code string) string {
	switch code {
	case "E_JOIN_LINK", "E_SERVICE_REQUEST":
		return "URL 或 Token 格式错误，请检查后重试"
	case "E_JOIN_TOKEN":
		return "Token 已过期或已使用，请联系管理员重新生成"
	case "E_JOIN_DOWNLOAD":
		return "连接加入服务器失败，请检查网络后重试"
	case "E_JOIN_CONFIG":
		return "收到的设备配置校验失败，请联系管理员"
	case "E_OPENVPN_CONFIG":
		return "请先输入 URL 和 Token 加入"
	case "E_OPENVPN_START", "E_OPENVPN_EXIT":
		return "VPN 启动失败，请重新连接；仍失败时重新安装客户端"
	case "E_OPENVPN_STOP":
		return "VPN 停止超时，请重启电脑后再试"
	default:
		return "EZGameLAN 操作失败，请稍后重试"
	}
}

type boundedLog struct {
	mu   sync.Mutex
	data []byte
}

func (b *boundedLog) Write(data []byte) (int, error) {
	const limit = 64 << 10
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(data) >= limit {
		b.data = append(b.data[:0], data[len(data)-limit:]...)
		return len(data), nil
	}
	if overflow := len(b.data) + len(data) - limit; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, data...)
	return len(data), nil
}
