package join

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/r3t2/ezgamelan/internal/config"
	"github.com/r3t2/ezgamelan/internal/invite"
)

const profileMediaType = "application/vnd.ezgamelan.profile+json"

func NewHTTPClient() *http.Client {
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          4,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		},
		Timeout: 20 * time.Second,
	}
}

func Fetch(ctx context.Context, client *http.Client, invitation Invitation) (invite.Profile, error) {
	if err := configForInvitation(invitation); err != nil {
		return invite.Profile{}, err
	}
	body, err := json.Marshal(struct {
		Token string `json:"token"`
	}{Token: invitation.Token})
	if err != nil {
		return invite.Profile{}, fmt.Errorf("E_JOIN_DOWNLOAD: 生成加入请求: %w", err)
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		invitation.URL,
		bytes.NewReader(body),
	)
	if err != nil {
		return invite.Profile{}, fmt.Errorf("E_JOIN_DOWNLOAD: 创建加入请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", profileMediaType)
	req.Header.Set("User-Agent", "EZGameLAN-Join/1")

	if client == nil {
		client = NewHTTPClient()
	}
	requestClient := *client
	requestClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := requestClient.Do(req)
	if err != nil {
		return invite.Profile{}, fmt.Errorf("E_JOIN_DOWNLOAD: 连接加入服务器: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return invite.Profile{}, errors.New("E_JOIN_TOKEN: 加入链接已失效，请联系管理员重新生成")
		}
		return invite.Profile{}, fmt.Errorf("E_JOIN_DOWNLOAD: 加入服务器返回 HTTP %d", resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != profileMediaType {
		return invite.Profile{}, errors.New("E_JOIN_DOWNLOAD: 服务器返回了错误的配置类型")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, invite.MaxSize+1))
	if err != nil {
		return invite.Profile{}, fmt.Errorf("E_JOIN_DOWNLOAD: 读取设备配置: %w", err)
	}
	if len(data) > invite.MaxSize {
		return invite.Profile{}, errors.New("E_JOIN_DOWNLOAD: 服务器返回的设备配置过大")
	}
	profile, err := invite.Parse(data, time.Now())
	if err != nil {
		return invite.Profile{}, fmt.Errorf("E_JOIN_CONFIG: 设备配置校验失败: %w", err)
	}
	return profile, nil
}

func LoadFile(path string) (invite.Profile, error) {
	file, err := os.Open(path)
	if err != nil {
		return invite.Profile{}, fmt.Errorf("E_JOIN_FILE: 打开邀请文件: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return invite.Profile{}, fmt.Errorf("E_JOIN_FILE: 检查邀请文件: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > invite.MaxSize {
		return invite.Profile{}, errors.New("E_JOIN_FILE: 邀请文件类型或大小错误")
	}
	data, err := io.ReadAll(io.LimitReader(file, invite.MaxSize+1))
	if err != nil {
		return invite.Profile{}, fmt.Errorf("E_JOIN_FILE: 读取邀请文件: %w", err)
	}
	profile, err := invite.Parse(data, time.Now())
	if err != nil {
		return invite.Profile{}, fmt.Errorf("E_JOIN_CONFIG: 邀请文件校验失败: %w", err)
	}
	return profile, nil
}

func configForInvitation(invitation Invitation) error {
	if invitation.URL == "" || invitation.Token == "" {
		return errors.New("E_JOIN_LINK: 加入链接内容不完整")
	}
	if err := config.ValidateEnrollURL(invitation.URL); err != nil {
		return fmt.Errorf("E_JOIN_LINK: 服务器地址校验失败: %w", err)
	}
	if err := invite.ValidateToken(invitation.Token); err != nil {
		return errors.New("E_JOIN_LINK: 加入凭据格式错误")
	}
	return nil
}
