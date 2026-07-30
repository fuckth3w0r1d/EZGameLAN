package setup

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

const maxProgressFileSize = 4096

type Progress struct {
	Stage   string
	Percent int
	Message string
}

var installProgress = map[string]Progress{
	"prepare":         {Stage: "prepare", Percent: 3, Message: "正在准备安装…"},
	"extract":         {Stage: "extract", Percent: 10, Message: "正在校验并解压安装包…"},
	"elevation":       {Stage: "elevation", Percent: 18, Message: "正在请求管理员权限…"},
	"validate":        {Stage: "validate", Percent: 24, Message: "正在检查 Windows 环境…"},
	"verify":          {Stage: "verify", Percent: 32, Message: "正在校验 OpenVPN 安装包…"},
	"check_openvpn":   {Stage: "check_openvpn", Percent: 38, Message: "正在检查 OpenVPN 和 TAP 驱动…"},
	"stop_client":     {Stage: "stop_client", Percent: 43, Message: "正在关闭现有 EZGameLAN 客户端…"},
	"stop_service":    {Stage: "stop_service", Percent: 48, Message: "正在停止现有 EZGameLAN 服务…"},
	"install_openvpn": {Stage: "install_openvpn", Percent: 56, Message: "正在安装或修复 OpenVPN 和 TAP 驱动…"},
	"copy_files":      {Stage: "copy_files", Percent: 70, Message: "正在安装 EZGameLAN 程序…"},
	"configure":       {Stage: "configure", Percent: 82, Message: "正在配置后台服务和启动入口…"},
	"start_service":   {Stage: "start_service", Percent: 92, Message: "正在启动 EZGameLAN 服务…"},
	"complete":        {Stage: "complete", Percent: 97, Message: "正在完成安装…"},
	"launch_client":   {Stage: "launch_client", Percent: 99, Message: "正在启动客户端…"},
	"finished":        {Stage: "finished", Percent: 100, Message: "安装完成"},
}

func ProgressForStage(stage string) Progress {
	return installProgress[stage]
}

func ReadProgress(path string) (Progress, error) {
	file, err := os.Open(path)
	if err != nil {
		return Progress{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Progress{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxProgressFileSize {
		return Progress{}, errors.New("install progress file is invalid")
	}
	scanner := bufio.NewScanner(file)
	var latest Progress
	for scanner.Scan() {
		stage := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\uFEFF"))
		progress, ok := installProgress[stage]
		if !ok {
			return Progress{}, fmt.Errorf("unknown install progress stage %q", stage)
		}
		if progress.Percent < latest.Percent {
			return Progress{}, errors.New("install progress moved backwards")
		}
		latest = progress
	}
	if err := scanner.Err(); err != nil {
		return Progress{}, err
	}
	return latest, nil
}
