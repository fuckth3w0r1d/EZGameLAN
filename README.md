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
2. 等待进度条依次完成安装包校验、OpenVPN/TAP 安装、程序复制、Service 配置和
   客户端启动；
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
Host ezlan-server
    HostName SERVER_HOST
    User SSH_USER
    IdentityFile ~/.ssh/id_ed25519
```

先在终端确认 SSH 密钥登录和服务器指纹：

```powershell
ssh ezlan-server
```

然后双击 `EZGameLAN-Admin.exe`：

1. 在原生连接窗口输入 SSH 配置别名，如 `ezlan-server`，或直接输入
   `SSH_USER@SERVER_HOST`；
2. 点击“连接”；
3. 管理端先验证 SSH、sudoers 和服务端管理协议；
4. 验证成功后自动打开仅监听 `127.0.0.1` 随机端口的 WebUI；
5. 最近一次验证成功的 SSH 目标会在下次启动时自动预填。

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

## 6. 仓库目录结构

```text
.
├── cmd/
│   ├── ezlanctl/              Linux 服务端管理 CLI 和加入服务
│   ├── ezgamelan-service/     Windows 后台 Service
│   ├── ezgamelan-client/      Windows 玩家界面
│   ├── ezgamelan-admin/       Windows 管理端启动器
│   ├── ezgamelan-setup/       Windows 单文件安装启动器
│   └── ezgamelan-pack/        安装包载荷封装工具
├── internal/
│   ├── admin/、adminweb/      服务端管理协议和本机 WebUI
│   ├── client/、ipc/          客户端状态机和 Named Pipe
│   ├── join/、netcfg/         加入流程和 TAP 网卡配置
│   ├── config/、store/        服务端配置和文件状态
│   ├── ezlan/、enroll/        设备管理和一次性领取服务
│   ├── invite/、pki/          邀请格式和证书生命周期
│   ├── openvpn/               OpenVPN 配置、状态和管理通道
│   └── atomicfile/、logging/、setup/
│                              原子写入、有界日志和安装包处理
├── packaging/
│   ├── linux/                 systemd、Nginx 和服务端安装脚本
│   └── windows/               Windows 构建、安装和卸载脚本
├── AGENTS.md                  项目开发约定
├── DESIGN.md                  架构决策和边界
├── go.mod、go.sum             Go 版本和锁定依赖
└── README.md                  部署与使用说明
```

构建产物统一写入被 Git 忽略的 `dist/`，运行配置、证书、邀请、日志和诊断文件
也不进入仓库。

## 7. 简要架构说明

```text
Windows 玩家电脑
  ezgamelan-client.exe
          │ Named Pipe
          ▼
  EZGameLAN Service ── openvpn.exe + TAP-Windows6
          │                         │
          │ HTTPS/443               │ OpenVPN TAP，UDP/21111
          ▼                         ▼
  Nginx 固定加入路径             Linux OpenVPN Server
          │                         │
          ▼                         └── 单个 10.86.0.0/24 虚拟 LAN
  ezlanctl serve（127.0.0.1:21112）

Windows 管理电脑
  EZGameLAN-Admin.exe
          │ 本机 127.0.0.1 随机端口 WebUI
          └── 系统 SSH ── sudo ezlanctl admin-stdio ── Linux 服务端
```

- **数据面：** OpenVPN TAP 负责二层虚拟局域网，所有玩家流量经中心服务器，
  项目自身不代理或解析游戏数据；
- **客户端边界：** 普通用户界面只发送受限 IPC 请求，唯一自有特权进程
  `EZGameLAN Service` 管理 OpenVPN、活动配置和 TAP 网卡；
- **加入流程：** 一次性 Token 通过 Nginx 的固定 HTTPS 路径领取设备配置，
  加入服务仅监听服务器回环地址，不接管现有静态站点；
- **管理流程：** WebUI 只在管理员电脑上按需监听回环地址，所有管理操作经系统
  SSH 和固定的 `ezlanctl admin-stdio` 命令执行；
- **服务端状态：** 配置、设备记录、证书和 CRL 使用本地文件保存，不引入数据库
  或公网管理 API；
- **运行恢复：** Linux 服务由 systemd 管理，Windows 连接由 Service 管理，
  状态轮询、重连、日志和诊断均设置上限。
