// Package join implements the small client-side invitation flow.
package join

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/r3t2/ezgamelan/internal/config"
	"github.com/r3t2/ezgamelan/internal/invite"
)

const maxLinkSize = 8 << 10

type Invitation struct {
	URL   string
	Token string
}

func NewInvitation(enrollURL, token string) (Invitation, error) {
	enrollURL = strings.TrimSpace(enrollURL)
	token = strings.TrimSpace(token)
	if err := config.ValidateEnrollURL(enrollURL); err != nil {
		return Invitation{}, fmt.Errorf("E_JOIN_LINK: 服务器地址校验失败: %w", err)
	}
	if err := invite.ValidateToken(token); err != nil {
		return Invitation{}, errors.New("E_JOIN_LINK: 加入凭据格式错误")
	}
	return Invitation{URL: enrollURL, Token: token}, nil
}

func ParseLink(raw string) (Invitation, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxLinkSize {
		return Invitation{}, errors.New("E_JOIN_LINK: 加入链接为空或过长")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return Invitation{}, errors.New("E_JOIN_LINK: 加入链接格式错误")
	}
	if parsed.Scheme != "ezgamelan" || parsed.Host != "join" ||
		parsed.User != nil || parsed.Port() != "" || parsed.Path != "" ||
		parsed.Opaque != "" {
		return Invitation{}, errors.New("E_JOIN_LINK: 加入链接目标错误")
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(values) != 1 {
		return Invitation{}, errors.New("E_JOIN_LINK: 加入链接参数错误")
	}
	urls, ok := values["url"]
	if !ok || len(urls) != 1 {
		return Invitation{}, errors.New("E_JOIN_LINK: 加入链接缺少服务器地址")
	}
	return NewInvitation(urls[0], parsed.Fragment)
}
