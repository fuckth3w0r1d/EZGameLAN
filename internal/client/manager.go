// Package client owns the single Windows-side OpenVPN process and its state.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/r3t2/ezgamelan/internal/ipc"
	"github.com/r3t2/ezgamelan/internal/join"
)

const maxRestartAttempts = 5

type Options struct {
	OwnerSID      string
	RuntimeDir    string
	OpenVPNPath   string
	ProfilesDir   string
	EnsureAdapter func(context.Context) (string, error)
	HTTPClient    *http.Client
	Logger        *slog.Logger
	ProcessLog    io.Writer
}

type processStopSignal interface {
	Bind(*exec.Cmd) error
	Stop() error
	Close()
}

type Manager struct {
	options Options

	operation   chan struct{}
	mu          sync.Mutex
	status      ipc.Status
	credentials []ipc.Credential
	store       *join.CredentialStore

	process     *exec.Cmd
	processStop processStopSignal
	processDone chan struct{}
	stopWanted  bool
	generation  uint64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	logger *slog.Logger
}

func New(options Options) (*Manager, error) {
	if !filepath.IsAbs(options.OpenVPNPath) || !filepath.IsAbs(options.RuntimeDir) {
		return nil, errors.New("E_INSTALL_PATH: OpenVPN 和运行目录必须是绝对路径")
	}

	ctx, cancel := context.WithCancel(context.Background())
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if options.ProcessLog == nil {
		options.ProcessLog = io.Discard
	}
	credentialStore, err := join.NewCredentialStore(options.ProfilesDir, options.OwnerSID)
	if err != nil {
		cancel()
		return nil, err
	}
	storedCredentials, err := credentialStore.List()
	if err != nil {
		cancel()
		return nil, err
	}
	manager := &Manager{
		operation:   make(chan struct{}, 1),
		options:     options,
		store:       credentialStore,
		credentials: ipcCredentials(storedCredentials),
		status: ipc.Status{
			State: ipc.StateNotConfigured,
		},
		ctx:    ctx,
		cancel: cancel,
		logger: logger,
	}
	if len(manager.credentials) > 0 {
		manager.status.State = ipc.StateDisconnected
	}
	return manager, nil
}

func (m *Manager) Handle(ctx context.Context, request ipc.Request) ipc.Response {
	var err error
	switch request.Action {
	case ipc.ActionJoin:
		err = m.Join(ctx, request.URL, request.Token)
	case ipc.ActionConnect:
		err = m.Connect(ctx, request.CredentialID)
	case ipc.ActionDisconnect:
		err = m.Disconnect(ctx)
	case ipc.ActionDeleteCredential:
		err = m.Delete(ctx, request.CredentialID)
	case ipc.ActionStatus:
	default:
		err = errors.New("E_SERVICE_REQUEST: 未知操作")
	}
	status, credentials := m.Snapshot()
	if err != nil {
		code := errorCode(err)
		m.logger.Error(
			"client operation failed",
			"action", request.Action,
			"code", code,
			"error", err,
		)
		return ipc.Response{
			Version:     ipc.Version,
			OK:          false,
			Code:        code,
			Message:     userMessage(code),
			Status:      status,
			Credentials: credentials,
		}
	}
	if request.Action != ipc.ActionStatus {
		m.logger.Info("client operation completed", "action", request.Action)
	}
	return ipc.Response{
		Version:     ipc.Version,
		OK:          true,
		Status:      status,
		Credentials: credentials,
	}
}

func (m *Manager) Join(ctx context.Context, enrollURL, token string) error {
	if err := m.acquireOperation(ctx); err != nil {
		return err
	}
	defer m.releaseOperation()

	invitation, err := join.NewInvitation(enrollURL, token)
	if err != nil {
		return err
	}
	if err := m.store.PrepareJoin(ctx); err != nil {
		return err
	}
	profile, err := join.Fetch(ctx, m.options.HTTPClient, invitation)
	if err != nil {
		return err
	}
	credential, err := m.store.Install(ctx, profile)
	if err != nil {
		return err
	}
	m.upsertCredential(credential)
	if err := m.stopProcess(ctx); err != nil {
		return err
	}
	return m.connectLocked(credential.ID)
}

func (m *Manager) Connect(ctx context.Context, credentialID string) error {
	if err := m.acquireOperation(ctx); err != nil {
		return err
	}
	defer m.releaseOperation()

	m.mu.Lock()
	exists := m.hasCredentialLocked(credentialID)
	m.mu.Unlock()
	if !exists {
		return errors.New("E_CREDENTIAL_NOT_FOUND: 所选凭证不存在")
	}
	m.mu.Lock()
	alreadyConnected := m.process != nil && m.status.CredentialID == credentialID
	switching := m.process != nil
	m.mu.Unlock()
	if alreadyConnected {
		return nil
	}
	if switching {
		if err := m.stopProcess(ctx); err != nil {
			return err
		}
	}
	return m.connectLocked(credentialID)
}

func (m *Manager) connectLocked(credentialID string) error {
	m.mu.Lock()
	if m.process != nil {
		m.mu.Unlock()
		return errors.New("E_OPENVPN_STATE: OpenVPN 仍在运行")
	}
	m.stopWanted = false
	m.status = ipc.Status{
		State:        ipc.StateDisconnected,
		CredentialID: credentialID,
	}
	command, stopSignal, done, generation, err := m.launchLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.wg.Add(1)
	go m.supervise(command, stopSignal, done, generation)
	return nil
}

func (m *Manager) Delete(ctx context.Context, credentialID string) error {
	if err := m.acquireOperation(ctx); err != nil {
		return err
	}
	defer m.releaseOperation()

	m.mu.Lock()
	exists := m.hasCredentialLocked(credentialID)
	active := m.status.CredentialID == credentialID
	running := m.process != nil
	if active && !running {
		m.stopWanted = true
	}
	m.mu.Unlock()
	if !exists {
		return errors.New("E_CREDENTIAL_NOT_FOUND: 所选凭证不存在")
	}
	if active && running {
		if err := m.stopProcess(ctx); err != nil {
			return err
		}
	}
	if err := m.store.Delete(credentialID); err != nil {
		return err
	}
	m.mu.Lock()
	filtered := m.credentials[:0]
	for _, credential := range m.credentials {
		if credential.ID != credentialID {
			filtered = append(filtered, credential)
		}
	}
	m.credentials = filtered
	if active {
		m.status = m.disconnectedStatusLocked("")
	} else if len(m.credentials) == 0 {
		m.status = ipc.Status{State: ipc.StateNotConfigured}
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) Disconnect(ctx context.Context) error {
	if err := m.acquireOperation(ctx); err != nil {
		return err
	}
	defer m.releaseOperation()
	return m.stopProcess(ctx)
}

func (m *Manager) acquireOperation(ctx context.Context) error {
	select {
	case m.operation <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.releaseOperation()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) releaseOperation() { <-m.operation }

func (m *Manager) stopProcess(ctx context.Context) error {
	m.mu.Lock()
	m.stopWanted = true
	command := m.process
	stopSignal := m.processStop
	done := m.processDone
	if command == nil {
		m.status = m.disconnectedStatusLocked(m.status.CredentialID)
		m.mu.Unlock()
		return nil
	}
	m.status = ipc.Status{
		State:        ipc.StateStopping,
		CredentialID: m.status.CredentialID,
	}
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
	status, _ := m.Snapshot()
	return status
}

func (m *Manager) Snapshot() (ipc.Status, []ipc.Credential) {
	m.mu.Lock()
	defer m.mu.Unlock()
	credentials := append([]ipc.Credential(nil), m.credentials...)
	return m.status, credentials
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
	credentialID := m.status.CredentialID
	if credentialID == "" {
		m.status = m.disconnectedStatusLocked("")
		return nil, nil, nil, 0, errors.New("E_OPENVPN_CONFIG: 请选择已保存的局域网")
	}
	configPath, err := m.store.RuntimeConfig(credentialID, m.options.RuntimeDir)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	if err := join.ProtectPath(m.ctx, configPath, false, ""); err != nil {
		return nil, nil, nil, 0, err
	}
	session, err := m.prepareManagement(configPath)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	command, stopSignal, err := newOpenVPNCommand(
		m.options.OpenVPNPath,
		configPath,
		session.arguments()...,
	)
	if err != nil {
		session.close()
		m.status = ipc.Status{
			State:        ipc.StateError,
			CredentialID: credentialID,
			ErrorCode:    "E_OPENVPN_START",
		}
		return nil, nil, nil, 0, fmt.Errorf("E_OPENVPN_START: 准备 OpenVPN: %w", err)
	}
	command.Stdout = bestEffortWriter{writer: m.options.ProcessLog}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		stopSignal.Close()
		session.close()
		m.status = ipc.Status{
			State:        ipc.StateError,
			CredentialID: credentialID,
			ErrorCode:    "E_OPENVPN_START",
		}
		return nil, nil, nil, 0, fmt.Errorf("E_OPENVPN_START: 启动 OpenVPN: %w", err)
	}
	if err := stopSignal.Bind(command); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		stopSignal.Close()
		session.close()
		return nil, nil, nil, 0, fmt.Errorf("E_OPENVPN_START: 绑定 VPN 生命周期: %w", err)
	}
	m.generation++
	generation := m.generation
	done := make(chan struct{})
	m.process = command
	m.processStop = stopSignal
	m.processDone = done
	m.status = ipc.Status{
		State:        ipc.StateConnecting,
		CredentialID: credentialID,
	}
	m.logger.Info("OpenVPN process started", "pid", command.Process.Pid)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.monitorManagement(session, generation, done)
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
		waitErr := current.Wait()
		currentStop.Close()

		m.mu.Lock()
		if m.generation == currentGeneration {
			m.process = nil
			m.processStop = nil
			m.processDone = nil
		}
		stop := m.stopWanted || m.ctx.Err() != nil
		if stop {
			m.logger.Info("OpenVPN process stopped")
			m.status = m.disconnectedStatusLocked(m.status.CredentialID)
			m.mu.Unlock()
			close(currentDone)
			return
		}
		m.logger.Warn(
			"OpenVPN process exited unexpectedly",
			"error", waitErr,
			"restart_attempt", restarts+1,
		)
		m.status = ipc.Status{
			State:        ipc.StateError,
			CredentialID: m.status.CredentialID,
			ErrorCode:    "E_OPENVPN_EXIT",
		}
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

func (m *Manager) disconnectedStatusLocked(credentialID string) ipc.Status {
	if len(m.credentials) == 0 {
		return ipc.Status{State: ipc.StateNotConfigured}
	}
	if !m.hasCredentialLocked(credentialID) {
		credentialID = ""
	}
	return ipc.Status{
		State:        ipc.StateDisconnected,
		CredentialID: credentialID,
	}
}

func (m *Manager) hasCredentialLocked(id string) bool {
	for _, credential := range m.credentials {
		if credential.ID == id {
			return true
		}
	}
	return false
}

func (m *Manager) upsertCredential(stored join.Credential) {
	credential := ipcCredential(stored)
	m.mu.Lock()
	found := false
	for index := range m.credentials {
		if m.credentials[index].ID == credential.ID {
			m.credentials[index] = credential
			found = true
			break
		}
	}
	if !found {
		m.credentials = append(m.credentials, credential)
	}
	if m.status.State == ipc.StateNotConfigured {
		m.status = ipc.Status{State: ipc.StateDisconnected}
	}
	sort.Slice(m.credentials, func(i, j int) bool {
		left, right := m.credentials[i], m.credentials[j]
		if left.Endpoint != right.Endpoint {
			return left.Endpoint < right.Endpoint
		}
		if left.ServerName != right.ServerName {
			return left.ServerName < right.ServerName
		}
		if left.ClientName != right.ClientName {
			return left.ClientName < right.ClientName
		}
		return left.ID < right.ID
	})
	m.mu.Unlock()
}

func ipcCredentials(stored []join.Credential) []ipc.Credential {
	if len(stored) == 0 {
		return nil
	}
	credentials := make([]ipc.Credential, len(stored))
	for index, credential := range stored {
		credentials[index] = ipcCredential(credential)
	}
	return credentials
}

func ipcCredential(stored join.Credential) ipc.Credential {
	return ipc.Credential{
		ID:         stored.ID,
		ClientName: stored.ClientName,
		Endpoint:   stored.Endpoint,
		ServerName: stored.ServerName,
	}
}

func restartDelay(attempt int) time.Duration {
	delay := time.Second << attempt
	if delay > 15*time.Second {
		return 15 * time.Second
	}
	return delay
}

type bestEffortWriter struct {
	writer io.Writer
}

func (w bestEffortWriter) Write(data []byte) (int, error) {
	_, _ = w.writer.Write(data)
	return len(data), nil
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
	case "E_CREDENTIAL_LIMIT":
		return "本机保存的局域网凭证已达到上限，请删除不用的凭证后重试"
	case "E_CREDENTIAL_NOT_FOUND", "E_CREDENTIAL_ID":
		return "所选凭证不存在，请刷新列表后重试"
	case "E_CREDENTIAL_READ":
		return "本机凭证已损坏，请删除后使用新邀请重新加入"
	case "E_CREDENTIAL_DELETE":
		return "删除本机凭证失败，请重启 EZGameLAN 后重试"
	case "E_OPENVPN_CONFIG":
		return "请选择一个已保存的局域网后连接"
	case "E_OPENVPN_START", "E_OPENVPN_EXIT":
		return "VPN 启动失败，请重新连接；仍失败时退出并重新解压完整便携包"
	case "E_ADAPTER":
		return "TAP 网卡准备失败，请导出日志查看具体错误"
	case "E_ADAPTER_REBOOT":
		return "TAP 驱动安装完成，请重启电脑后连接所选局域网"
	case "E_OPENVPN_MANAGEMENT":
		return "无法确认 VPN 连接状态，请重新连接并导出日志"
	case "E_OPENVPN_STOP":
		return "VPN 停止超时，请重启电脑后再试"
	default:
		return "EZGameLAN 操作失败，请稍后重试"
	}
}
