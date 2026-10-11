package admin

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/r3t2/ezgamelan/internal/config"
	"github.com/r3t2/ezgamelan/internal/ezlan"
	"github.com/r3t2/ezgamelan/internal/invite"
	"github.com/r3t2/ezgamelan/internal/openvpn"
	"github.com/r3t2/ezgamelan/internal/store"
)

const operationTimeout = 30 * time.Second

type manager interface {
	Config() config.Config
	CRLWarning() string
	Invite(context.Context, string) (ezlan.InviteResult, error)
	Revoke(string) error
	List() ([]store.Client, error)
}

type host interface {
	Sessions(context.Context) ([]openvpn.Session, error)
	Disconnect(context.Context, uint64) error
	ServiceState(context.Context, string) (string, error)
	RestartService(context.Context, string) error
	Diagnostics(context.Context) (string, error)
}

type Controller struct {
	manager manager
	host    host
	now     func() time.Time
}

func NewController(manager manager, host host) *Controller {
	return &Controller{manager: manager, host: host, now: time.Now}
}

func OpenController(configPath string) (*Controller, error) {
	manager, err := ezlan.Open(configPath)
	if err != nil {
		return nil, err
	}
	return NewController(manager, newHost()), nil
}

func (c *Controller) Execute(parent context.Context, request Request) Response {
	if request.ProtocolVersion != ProtocolVersion {
		return failure("E_CONFIG_PROTOCOL", "管理端与服务器协议版本不一致，请更新管理端")
	}
	if request.Action == "" {
		return failure("E_CONFIG_ACTION", "管理操作为空")
	}
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()

	switch request.Action {
	case ActionSnapshot:
		if request.Name != "" {
			return failure("E_CONFIG_FIELD", "查看状态请求包含多余字段")
		}
		return c.snapshot(ctx)
	case ActionInvite:
		return c.invite(ctx, request.Name)
	case ActionRevoke:
		return c.revoke(ctx, request.Name)
	case ActionDisconnect:
		return c.disconnect(ctx, request.Name)
	case ActionRestartOpenVPN:
		if request.Name != "" {
			return failure("E_CONFIG_FIELD", "重启请求包含多余字段")
		}
		return c.restart(ctx, OpenVPNUnit, "OpenVPN 服务已重启")
	case ActionRestartEnroll:
		if request.Name != "" {
			return failure("E_CONFIG_FIELD", "重启请求包含多余字段")
		}
		return c.restart(ctx, EnrollUnit, "加入服务已重启")
	case ActionDiagnostics:
		if request.Name != "" {
			return failure("E_CONFIG_FIELD", "诊断请求包含多余字段")
		}
		return c.diagnostics(ctx)
	default:
		return failure("E_CONFIG_ACTION", "未知的管理操作")
	}
}

func (c *Controller) snapshot(ctx context.Context) Response {
	clients, err := c.manager.List()
	if err != nil {
		return internalError("E_CONFIG_CLIENTS", "读取设备记录失败", err)
	}
	sessions, sessionErr := c.host.Sessions(ctx)
	sessionByName := make(map[string]openvpn.Session, len(sessions))
	for _, session := range sessions {
		sessionByName[session.Name] = session
	}
	openVPNState, openVPNErr := c.host.ServiceState(ctx, OpenVPNUnit)
	if openVPNErr != nil {
		openVPNState = "unknown"
	}
	enrollState, enrollErr := c.host.ServiceState(ctx, EnrollUnit)
	if enrollErr != nil {
		enrollState = "unknown"
	}

	devices := make([]Device, 0, len(clients))
	for _, client := range clients {
		device := Device{
			Name:      client.Name,
			Status:    client.Status,
			CreatedAt: client.CreatedAt,
			NotAfter:  client.NotAfter,
		}
		if session, ok := sessionByName[client.Name]; ok {
			connected := session.ConnectedSince
			device.Online = true
			device.VirtualAddress = session.VirtualAddress
			device.RealAddress = session.RealAddress
			device.BytesReceived = session.BytesReceived
			device.BytesSent = session.BytesSent
			device.ConnectedSince = &connected
		}
		devices = append(devices, device)
	}

	cfg := c.manager.Config()
	response := success()
	response.Snapshot = &Snapshot{
		GeneratedAt:  c.now().UTC(),
		PublicHost:   cfg.PublicHost,
		Endpoint:     cfg.OpenVPNEndpoint(),
		EnrollURL:    cfg.EnrollURL,
		VirtualCIDR:  cfg.VirtualCIDR,
		OpenVPNState: openVPNState,
		EnrollState:  enrollState,
		Devices:      devices,
	}
	if sessionErr != nil {
		response.Snapshot.Warning = "在线状态暂时读取失败，设备凭据状态仍然有效"
	} else if openVPNErr != nil || enrollErr != nil {
		response.Snapshot.Warning = "部分 systemd 服务状态暂时读取失败"
	}
	response.Snapshot.Warning = strings.TrimSpace(response.Snapshot.Warning + " " + c.manager.CRLWarning())
	return response
}

func (c *Controller) invite(ctx context.Context, name string) Response {
	if !invite.ValidName(name) {
		return failure("E_CONFIG_NAME", "设备名只允许 1 到 32 位字母、数字、短横线和下划线")
	}
	result, err := c.manager.Invite(ctx, name)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrInviteExists):
			return failure("E_CERT_EXISTS", "同名设备已存在，请先吊销旧设备")
		case errors.Is(err, store.ErrClientLimit):
			return failure("E_CONFIG_LIMIT", "当前虚拟局域网已达到 10 台设备上限")
		default:
			return internalError("E_CERT_INVITE", "生成加入链接失败", err)
		}
	}
	response := success()
	response.Invite = &Invite{Name: name, Link: result.Link, ExpiresAt: result.ExpiresAt}
	response.Message = "一次性加入链接已生成"
	return response
}

func (c *Controller) revoke(ctx context.Context, name string) Response {
	if !invite.ValidName(name) {
		return failure("E_CONFIG_NAME", "设备名格式错误")
	}
	if err := c.manager.Revoke(name); err != nil {
		if errors.Is(err, store.ErrRevocationPending) {
			return failure("E_CERT_REVOKE_PENDING", "吊销已记录但尚未完全生效，请重试吊销并检查维护任务")
		}
		if errors.Is(err, store.ErrNotFound) {
			return failure("E_CERT_NOT_FOUND", "设备不存在")
		}
		return internalError("E_CERT_REVOKE", "吊销设备失败", err)
	}
	disconnected, err := c.disconnectByName(ctx, name)
	response := success()
	response.Message = "设备凭据已吊销"
	if err != nil {
		response.Warning = "凭据已吊销；在线连接状态读取失败，连接会在下次认证时失效"
	} else if disconnected > 0 {
		response.Message = "设备凭据已吊销，现有连接已断开"
	}
	return response
}

func (c *Controller) disconnect(ctx context.Context, name string) Response {
	if !invite.ValidName(name) {
		return failure("E_CONFIG_NAME", "设备名格式错误")
	}
	count, err := c.disconnectByName(ctx, name)
	if err != nil {
		return internalError("E_OPENVPN_MANAGEMENT", "断开设备失败", err)
	}
	if count == 0 {
		return failure("E_OPENVPN_OFFLINE", "设备当前不在线")
	}
	response := success()
	response.Message = "设备连接已断开，凭据仍然有效"
	return response
}

func (c *Controller) disconnectByName(ctx context.Context, name string) (int, error) {
	sessions, err := c.host.Sessions(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, session := range sessions {
		if session.Name != name {
			continue
		}
		if err := c.host.Disconnect(ctx, session.ClientID); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (c *Controller) restart(ctx context.Context, unit, message string) Response {
	if err := c.host.RestartService(ctx, unit); err != nil {
		return internalError("E_SERVICE_RESTART", "重启服务失败", err)
	}
	response := success()
	response.Message = message
	return response
}

func (c *Controller) diagnostics(ctx context.Context) Response {
	diagnostics, err := c.host.Diagnostics(ctx)
	if err != nil {
		return internalError("E_INTERNAL_DIAGNOSTICS", "读取服务诊断失败", err)
	}
	response := success()
	response.Diagnostics = diagnostics
	response.Message = "已收集最近的有界诊断信息"
	return response
}

func internalError(code, message string, _ error) Response {
	// Keep filesystem paths, command details and other internal causes out of
	// the protocol response. The stable code identifies the failing operation.
	return failure(code, message)
}
