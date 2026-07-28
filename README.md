# EZGameLAN

## 1. 项目使用场景

EZGameLAN 是面向个人和朋友联机的轻量虚拟局域网工具，适合 Minecraft Java
Edition、L4D2 等支持局域网或 IP 直连的游戏。

- 单个虚拟局域网，最多 10 台 Windows 客户端；
- 默认虚拟网段为 `10.86.0.0/24`；
- 所有虚拟局域网流量经中心 Linux 服务器转发；
- 支持广播、多播和虚拟 IP 直连；
- 客户端保持系统默认路由和 DNS；
- 玩家通过一次性加入链接连接，无需接触 OpenVPN 配置。

## 2. 项目技术栈

- **数据面：** OpenVPN 2.6 TAP、TAP-Windows6、`client-to-client`；
- **服务端：** Linux、systemd、Go、文件状态；
- **公网入口：** Nginx HTTPS，仅代理固定加入路径；
- **Windows 客户端：** Go、Windows Service、原生 Win32 界面、Named Pipe；
- **管理端：** Go 内嵌 HTML/CSS/JavaScript、本机回环 WebUI、系统 OpenSSH；
- **状态管理：** JSON 文件、证书和 CRL，不使用数据库；
- **主要 Go 依赖：** 标准库和 `golang.org/x/sys/windows`。

## 3. 服务端部署方法

服务端需要 Linux、systemd、Nginx 和 OpenVPN 2.6。先在本地构建 Linux x64
二进制：

```bash
GOCACHE=/tmp/ezgamelan-gocache \
GOMODCACHE=/tmp/ezgamelan-gomodcache \
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
go build -trimpath \
  -o dist/ezlanctl-linux-amd64 \
  ./cmd/ezlanctl
```

将仓库和构建产物同步到服务器后执行：

```bash
sudo ./packaging/linux/install.sh \
  --host VPN_HOST \
  --enroll-url https://WEB_HOST/ezgamelan/v1/enroll \
  --admin-user SSH_USER \
  --binary ./dist/ezlanctl-linux-amd64
```

将以下片段包含到现有 Nginx HTTPS `server` 块：

```text
packaging/linux/nginx-ezgamelan-location.conf
```

检查并重载 Nginx：

```bash
sudo nginx -t
sudo systemctl reload nginx
```

云防火墙开放：

```text
21111/UDP    OpenVPN
443/TCP      HTTPS 加入链接
```

检查服务：

```bash
systemctl status ezgamelan-openvpn.service
systemctl status ezgamelan-enroll.service
```

常用服务端命令：

```bash
sudo ezlanctl status
sudo ezlanctl list
sudo ezlanctl invite DEVICE_NAME
sudo ezlanctl revoke DEVICE_NAME
```

## 4. 客户端使用方法

Windows x64 客户端使用单文件安装包：

```text
EZGameLAN-Setup.exe
```

安装和加入步骤：

1. 双击安装包并确认一次 UAC；
2. 打开 EZGameLAN；
3. 将管理员提供的完整 `ezgamelan://` 加入链接粘贴到输入框；
4. 点击“加入并连接”；
5. 显示“已连接”和 `10.86.0.x` 虚拟 IP 后启动游戏；
6. 游戏优先使用其他玩家的虚拟 IP 直连。

客户端也接受用空格或换行分隔的 HTTPS URL 与 Token。完成首次加入后，只需使用
“连接”和“断开”。虚拟网卡友好名称为 `EZGameLAN`。

遇到连接问题时点击“导出日志”。导出内容包含最近的有界、脱敏客户端与 Service
日志。

生成 Windows 安装包：

```bash
./packaging/windows/build-release.sh \
  --openvpn-msi /tmp/OpenVPN-2.6.21-I001-amd64.msi
```

## 5. 管理端配置使用方法

构建 Windows x64 管理端：

```bash
./packaging/windows/build-admin.sh
```

生成文件：

```text
dist/EZGameLAN-Admin.exe
```

在管理员 Windows 电脑安装或启用 OpenSSH Client，并配置
`%USERPROFILE%\.ssh\config`：

```sshconfig
Host s0
    HostName SERVER_HOST
    User SSH_USER
    IdentityFile ~/.ssh/id_ed25519
```

先在终端确认 SSH 密钥登录和服务器指纹：

```powershell
ssh s0
```

然后双击 `EZGameLAN-Admin.exe`。浏览器会打开仅监听
`127.0.0.1` 随机端口的管理界面，默认 SSH 别名为 `s0`。

管理界面支持：

- 查看 OpenVPN、加入服务和设备状态；
- 查看在线设备、虚拟 IP、连接时间和流量；
- 生成并复制一次性加入链接；
- 断开在线设备；
- 吊销设备并立即断开连接；
- 重启 OpenVPN 或加入服务；
- 查看和导出有界、脱敏的服务端诊断。

服务器端管理权限由部署参数 `--admin-user SSH_USER` 配置，只允许执行：

```text
sudo -n /usr/local/bin/ezlanctl admin-stdio
```
