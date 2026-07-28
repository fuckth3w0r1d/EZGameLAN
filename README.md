# EZGameLAN

EZGameLAN 是一个面向个人和朋友的轻量虚拟局域网工具。数据面使用中心
OpenVPN TAP 服务器，首版支持单个 LAN 和最多 10 台 Windows 客户端。

当前已经实现服务端最小闭环：

```text
go build -o ezlanctl ./cmd/ezlanctl

sudo ./ezlanctl init --host HOST
sudo ./ezlanctl invite alice
sudo ./ezlanctl serve
sudo ./ezlanctl revoke alice
sudo ./ezlanctl list
sudo ./ezlanctl status
```

`init` 会生成本地 CA、OpenVPN 服务端配置和 `tls-crypt-v2` 密钥。`invite`
默认生成 30 分钟有效的“HTTPS URL + Token”一次性加入链接，也可通过
`--file PATH` 生成离线 `.ezlan`。公网 HTTPS 由已有 Nginx 提供，Go 加入服务
只监听 `127.0.0.1:21112`。

Windows 端使用自己的简易客户端和后台 Service，不依赖 OpenVPN GUI：

- `ezgamelan-client.exe` 是普通用户托盘界面，只显示 URL、Token、连接、断开和
  状态；
- `ezgamelan-service.exe` 是唯一的自有特权进程；
- 客户端与 Service 使用受限、版本化、定长上限的本机 Named Pipe；
- Service 通过系统证书库访问 HTTPS，在请求体中提交一次性 Token；
- 领取到的 `.ezlan` 经过严格校验后才生成固定的 `ezgamelan.ovpn`；
- 活动配置保存在 `%ProgramData%\EZGameLAN\config`，仅 Administrators 和
  `SYSTEM` 可读；
- Service 直接启动一个 `openvpn.exe`，检查虚拟 IP，并对异常退出做有上限的
  重试。

Windows x64 开发构建：

```powershell
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"

New-Item -ItemType Directory -Force dist | Out-Null
go build -trimpath -o dist/ezgamelan-service.exe ./cmd/ezgamelan-service
go build -trimpath -ldflags "-H=windowsgui" `
  -o dist/ezgamelan-client.exe ./cmd/ezgamelan-client
```

TAP-Windows6 驱动需要在安装阶段一次性安装。Service 启动后，玩家只运行
`ezgamelan-client.exe`，输入管理员给出的 HTTPS URL 和 Token，然后点击
“加入并连接”。

当前 Windows 代码已经完成 URL + Token 领取、严格配置生成、Named Pipe、
Service 生命周期和原生 Win32 界面。发布安装包前还需在 Windows 10/11 真机上
完成 TAP 安装、Service 安装/卸载、签名和游戏联机验证。

最终分发物是单个 `EZGameLAN-Setup.exe`。玩家只需双击这个文件并确认一次
UAC，不接触 PowerShell 脚本、MSI 或内部文件。

在 Linux 开发机生成安装包：

```bash
./packaging/windows/build-release.sh \
  --openvpn-msi /tmp/OpenVPN-2.6.21-I001-amd64.msi
```

也可在 Windows 开发机生成：

```powershell
.\packaging\windows\build-release.ps1 `
  -OpenVPNMSI PATH\OpenVPN-2.6.21-I001-amd64.msi
```

构建过程交叉编译客户端、Service 和安装启动器，将固定 OpenVPN MSI 以及来源、
许可证和 SHA-256 封装进一个 EXE。启动器先校验自身载荷；安装阶段再次校验
OpenVPN MSI 的固定 SHA-256 和 Authenticode 签名，只选择 OpenVPN 核心与
TAP-Windows6。安装器同时注册 EZGameLAN Service、开始菜单入口和
`ezgamelan://` 协议，卸载入口出现在 Windows“已安装的应用”中。

开发检查：

```bash
gofmt -w ./cmd ./internal
go test ./...
go vet ./...

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -o /tmp/ezgamelan-service.exe ./cmd/ezgamelan-service
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags '-H=windowsgui' \
  -o /tmp/ezgamelan-client.exe ./cmd/ezgamelan-client
```

Linux 服务器部署：

```text
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -o dist/ezlanctl-linux-amd64 ./cmd/ezlanctl

sudo ./packaging/linux/install.sh \
  --host VPN_HOST \
  --enroll-url https://WEB_HOST/ezgamelan/v1/enroll \
  --binary ./dist/ezlanctl-linux-amd64
```

安装器保留已经存在的 CA 和配置，安装并启动 OpenVPN 与一次性加入服务。
首次从旧版配置升级时会保留 `/etc/ezgamelan/config.json.v1.bak`，回滚旧二进制
时可恢复该文件。

将 [`packaging/linux/nginx-ezgamelan-location.conf`](packaging/linux/nginx-ezgamelan-location.conf)
包含到现有 HTTPS `server` 块，然后先执行 `nginx -t` 再 reload。该片段只精确
匹配 `/ezgamelan/v1/enroll`，不会接管静态网页的 `location /`。

云防火墙只需额外开放 OpenVPN 的 `21111/UDP`；注册领取复用已有 `443/TCP`。

架构和开发约束分别见 [DESIGN.md](DESIGN.md) 与 [AGENTS.md](AGENTS.md)。
