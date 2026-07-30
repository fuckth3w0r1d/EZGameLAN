// Package client owns the single Windows-side OpenVPN process and its state.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/r3t2/ezgamelan/internal/atomicfile"
	"github.com/r3t2/ezgamelan/internal/ipc"
	"github.com/r3t2/ezgamelan/internal/join"
	"github.com/r3t2/ezgamelan/internal/netcfg"
)

const maxRestartAttempts = 5

type Options struct {
	OpenVPNPath       string
	LegacyConfigPath  string
	AdapterMarkerPath string
	HTTPClient        *http.Client
	Logger            *slog.Logger
	ProcessLog        io.Writer
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
	credentials []ipc.Credential
	store       *join.CredentialStore

	activeConfigPath string
	process          *exec.Cmd
	processStop      processStopSignal
	processDone      chan struct{}
	stopWanted       bool
	generation       uint64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	logger *slog.Logger
}

func New(options Options) (*Manager, error) {
	if !filepath.IsAbs(options.OpenVPNPath) || !filepath.IsAbs(options.LegacyConfigPath) {
		return nil, errors.New("E_INSTALL_PATH: OpenVPN 和配置路径必须是绝对路径")
	}
	if options.AdapterMarkerPath != "" && !filepath.IsAbs(options.AdapterMarkerPath) {
		return nil, errors.New("E_INSTALL_PATH: 网卡清理标记路径必须是绝对路径")
	}
	ctx, cancel := context.WithCancel(context.Background())
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if options.ProcessLog == nil {
		options.ProcessLog = io.Discard
	}
	credentialStore, err := join.NewCredentialStore(options.LegacyConfigPath)
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
	credential, err := m.store.Install(ctx, profile)
	if err != nil {
		return err
	}
	m.upsertCredential(credential)
	configPath, err := m.store.ConfigPath(credential.ID)
	if err != nil {
		return err
	}
	return m.connectLocked(credential.ID, configPath)
}

func (m *Manager) Connect(ctx context.Context, credentialID string) error {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()

	m.mu.Lock()
	exists := m.hasCredentialLocked(credentialID)
	m.mu.Unlock()
	if !exists {
		return errors.New("E_CREDENTIAL_NOT_FOUND: 所选凭证不存在")
	}
	configPath, err := m.store.ConfigPath(credentialID)
	if err != nil {
		return err
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
	return m.connectLocked(credentialID, configPath)
}

func (m *Manager) connectLocked(credentialID, configPath string) error {
	m.mu.Lock()
	if m.process != nil {
		m.mu.Unlock()
		return errors.New("E_OPENVPN_STATE: OpenVPN 仍在运行")
	}
	m.stopWanted = false
	m.activeConfigPath = configPath
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
	m.operationMu.Lock()
	defer m.operationMu.Unlock()

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
		m.activeConfigPath = ""
		m.status = m.disconnectedStatusLocked("")
	} else if len(m.credentials) == 0 {
		m.activeConfigPath = ""
		m.status = ipc.Status{State: ipc.StateNotConfigured}
	}
	m.mu.Unlock()
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
	if credentialID == "" || m.activeConfigPath == "" {
		m.status = m.disconnectedStatusLocked("")
		return nil, nil, nil, 0, errors.New("E_OPENVPN_CONFIG: 请选择已保存的局域网")
	}
	command, stopSignal, err := newOpenVPNCommand(
		m.options.OpenVPNPath,
		m.activeConfigPath,
	)
	if err != nil {
		m.status = ipc.Status{
			State:        ipc.StateError,
			CredentialID: credentialID,
			ErrorCode:    "E_OPENVPN_START",
		}
		return nil, nil, nil, 0, fmt.Errorf("E_OPENVPN_START: 准备 OpenVPN: %w", err)
	}
	command.Env = minimalEnvironment()
	command.Stdout = bestEffortWriter{writer: m.options.ProcessLog}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		stopSignal.Close()
		m.status = ipc.Status{
			State:        ipc.StateError,
			CredentialID: credentialID,
			ErrorCode:    "E_OPENVPN_START",
		}
		return nil, nil, nil, 0, fmt.Errorf("E_OPENVPN_START: 启动 OpenVPN: %w", err)
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

func (m *Manager) monitorVirtualIP(generation uint64, done <-chan struct{}) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ip, adapterName, tapCandidate := findVirtualInterface()
			if ip == "" {
				continue
			}
			adapterName = m.renameVirtualAdapter(adapterName, tapCandidate)
			connected := false
			m.mu.Lock()
			if m.generation == generation && m.process != nil {
				m.status = ipc.Status{
					State:        ipc.StateConnected,
					CredentialID: m.status.CredentialID,
					VirtualIP:    ip,
					AdapterName:  adapterName,
				}
				connected = true
			}
			m.mu.Unlock()
			if connected {
				m.logger.Info(
					"virtual LAN connected",
					"virtual_ip", ip,
					"adapter", adapterName,
				)
			}
			return
		case <-done:
			return
		case <-m.ctx.Done():
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
		if left.Legacy != right.Legacy {
			return left.Legacy
		}
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
		Legacy:     stored.Legacy,
	}
}

func findVirtualInterface() (string, string, bool) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", "", false
	}
	for _, networkInterface := range interfaces {
		addresses, err := networkInterface.Addrs()
		if err != nil {
			continue
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
				hardwareAddress := networkInterface.HardwareAddr
				tapCandidate := networkInterface.Name == "OpenVPN TAP-Windows6" ||
					networkInterface.Name == netcfg.AdapterName ||
					(len(hardwareAddress) == 6 &&
						hardwareAddress[0] == 0x00 &&
						hardwareAddress[1] == 0xff)
				if !tapCandidate {
					continue
				}
				return ip4.String(), networkInterface.Name, tapCandidate
			}
		}
	}
	return "", "", false
}

func (m *Manager) renameVirtualAdapter(currentName string, tapCandidate bool) string {
	if currentName == "" || currentName == netcfg.AdapterName {
		return currentName
	}
	if !tapCandidate {
		m.logger.Warn(
			"adapter rename skipped because TAP identity was not confirmed",
			"adapter", currentName,
		)
		return currentName
	}
	markerPath := m.options.AdapterMarkerPath
	if markerPath == "" {
		m.logger.Warn(
			"adapter rename skipped because cleanup marker is unavailable",
			"adapter", currentName,
		)
		return currentName
	}
	createdMarker := false
	if info, err := os.Lstat(markerPath); errors.Is(err, os.ErrNotExist) {
		if err := atomicfile.Create(markerPath, []byte(currentName+"\n"), 0o600); err != nil {
			m.logger.Warn("adapter rename marker failed", "error", err)
			return currentName
		}
		createdMarker = true
	} else if err != nil || !info.Mode().IsRegular() {
		m.logger.Warn("adapter rename marker is invalid", "error", err)
		return currentName
	}

	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	if err := netcfg.RenameAdapter(ctx, currentName, netcfg.AdapterName); err != nil {
		if createdMarker {
			_ = os.Remove(markerPath)
		}
		m.logger.Warn(
			"adapter rename failed",
			"adapter", currentName,
			"error", err,
		)
		return currentName
	}
	m.logger.Info(
		"virtual adapter renamed",
		"from", currentName,
		"to", netcfg.AdapterName,
	)
	return netcfg.AdapterName
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
		return "VPN 启动失败，请重新连接；仍失败时重新安装客户端"
	case "E_OPENVPN_STOP":
		return "VPN 停止超时，请重启电脑后再试"
	default:
		return "EZGameLAN 操作失败，请稍后重试"
	}
}
