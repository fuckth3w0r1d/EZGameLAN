package adminweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/r3t2/ezgamelan/internal/admin"
	"github.com/r3t2/ezgamelan/internal/logging"
)

const sshOutputLimit = admin.MaxResponseSize

type Remote interface {
	Call(context.Context, string, admin.Request) (admin.Response, error)
}

type SSHRemote struct {
	path string
}

func NewSSHRemote(path string) (*SSHRemote, error) {
	if path == "" {
		var err error
		path, err = exec.LookPath("ssh")
		if err != nil {
			return nil, errors.New("未找到系统 ssh，请安装或启用 OpenSSH 客户端")
		}
	}
	return &SSHRemote{path: path}, nil
}

func (s *SSHRemote) Call(
	ctx context.Context,
	server string,
	request admin.Request,
) (admin.Response, error) {
	if err := ValidateServerAlias(server); err != nil {
		return admin.Response{}, err
	}
	var input bytes.Buffer
	if err := json.NewEncoder(&input).Encode(request); err != nil {
		return admin.Response{}, fmt.Errorf("编码管理请求: %w", err)
	}
	if input.Len() > admin.MaxRequestSize {
		return admin.Response{}, errors.New("管理请求超过大小限制")
	}

	command := exec.CommandContext(
		ctx,
		s.path,
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		server,
		"sudo", "-n", "/usr/local/bin/ezlanctl", "admin-stdio",
	)
	command.Stdin = &input
	var stdout, stderr boundedBuffer
	stdout.limit = sshOutputLimit
	stderr.limit = 8 << 10
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(logging.Sanitize(stderr.Bytes()))
		if len(detail) > 2048 {
			detail = detail[len(detail)-2048:]
		}
		if detail == "" {
			detail = err.Error()
		}
		return admin.Response{}, fmt.Errorf(
			"SSH 连接或远端管理命令失败：%s；请先在终端确认该别名可使用密钥登录",
			detail,
		)
	}

	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	var response admin.Response
	if err := decoder.Decode(&response); err != nil {
		return admin.Response{}, fmt.Errorf("解析服务器响应: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return admin.Response{}, errors.New("服务器返回了多余数据")
		}
		return admin.Response{}, fmt.Errorf("检查服务器响应: %w", err)
	}
	if response.ProtocolVersion != admin.ProtocolVersion {
		return admin.Response{}, errors.New("管理端与服务器协议版本不一致，请更新服务器")
	}
	return response, nil
}

func ValidateServerAlias(value string) error {
	if len(value) == 0 || len(value) > 128 || value[0] == '-' {
		return errors.New("SSH 别名长度或格式错误")
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '.' || char == '_' || char == '-' || char == '@' {
			continue
		}
		return errors.New("SSH 别名只允许字母、数字、点、下划线、短横线和 @")
	}
	return nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		remaining := b.limit - b.Len()
		if remaining > 0 {
			_, _ = b.Buffer.Write(data[:remaining])
		}
		return len(data), errors.New("SSH 输出超过大小限制")
	}
	return b.Buffer.Write(data)
}
