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
- **Windows 客户端：** Go、原生 Win32 界面、按需提权后台、Named Pipe；
- **管理端：** Go 内嵌 HTML/CSS/JavaScript、本机回环 WebUI、系统 OpenSSH；
- **状态管理：** JSON 文件、证书和 CRL，不使用数据库；
- **主要 Go 依赖：** 标准库和 `golang.org/x/sys/windows`。

## 3. 服务端部署方法

服务端需要 Linux、systemd、Nginx 和 OpenVPN 2.6。先在本地构建 Linux x64
二进制：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
go build -trimpath \
  -o dist/ezlanctl-linux-amd64 \
  ./cmd/ezlanctl
```

将仓库和构建产物同步到服务器后执行：

```bash
sudo ./packaging/server/install.sh \
  --host VPN_HOST \
  --enroll-url https://WEB_HOST/ezgamelan/v1/enroll \
  --admin-user SSH_USER \
  --binary ./dist/ezlanctl-linux-amd64
```

将以下片段包含到现有 Nginx HTTPS `server` 块：

```text
packaging/server/nginx-ezgamelan-location.conf
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
systemctl status ezgamelan-maintain.timer
```

常用服务端命令：

```bash
sudo ezlanctl status
sudo ezlanctl list
sudo ezlanctl invite DEVICE_NAME
sudo ezlanctl revoke DEVICE_NAME
sudo ezlanctl maintain
```

维护定时器每天检查吊销记录和 CRL，在 CRL 剩余有效期不足 30 天时续期。OpenVPN
启动前也会执行维护，恢复尚未完成的吊销发布。管理界面会提示 CRL 异常；吊销操作
若提示发布失败，应排除文件权限或磁盘问题后执行 `sudo ezlanctl maintain`。

更新 VPN 公网地址或加入 URL：

```bash
sudo ezlanctl config-update \
  --host VPN_HOST \
  --enroll-url https://WEB_HOST/ezgamelan/v1/enroll
sudo systemctl restart ezgamelan-enroll.service
```

该命令保留 CA、设备身份和吊销状态，只影响后续生成的邀请及凭证。已保存的凭证和
已有邀请仍包含原地址，应保留原域名解析，或安排玩家使用新邀请重新加入。管理端
需重新连接以读取更新后的配置。重复运行部署脚本不能代替地址更新。

## 4. 客户端使用方法

Windows x64 客户端使用目录式便携包，运行环境为 Windows 10/11：

```text
dist/EZGameLAN-Portable.zip
```

解压和加入步骤：

1. 完整解压到当前用户可写、支持 Windows ACL 的本地目录，如
   `PortableSoftware/EZGameLAN`，路径中不使用符号链接或目录重定向；
2. 双击 `EZGameLAN.exe`，保留同目录的 `EZGameLAN-Backend.exe` 和 `openvpn/`；
3. 将管理员提供的完整 `ezgamelan://` 加入链接粘贴到“添加局域网”输入框；
4. 点击“添加并连接”；
5. 首次连接时确认 UAC；当前运行期间的后续连接复用该后台。全新电脑会按需安装
   TAP 驱动并创建专用网卡；如系统要求重启，重启后再次连接；
6. 显示“已连接”和服务器分配的虚拟 IP（默认 `10.86.0.x`）后启动游戏；
7. 游戏优先使用其他玩家的虚拟 IP 直连。

客户端也接受用空格或换行分隔的 HTTPS URL 与 Token。领取成功后，凭证会出现在
“已保存的局域网”列表；以后启动时选择它并点击“连接所选”，无需再次使用邀请
链接。客户端最多保存 32 个局域网凭证，同一时间只连接一个。切换连接前会先保存
新领取的凭证；即使随后连接失败，也可从列表重试。删除凭证只清理本机配置，需要
再次加入时应使用新的邀请链接。专用网卡名称为 `EZGameLAN-` 加随机后缀，不复用
其他软件的 TAP 网卡。同一台电脑同时只允许一个 EZGameLAN 连接后台。

凭证和客户端日志位于便携目录的 `data/`。退出后可在同一台电脑、同一 Windows
用户下整体移动目录；升级时退出程序，
用新包替换两份 EXE 和 `openvpn/`，保留 `data/`。凭证包含设备私钥，不应分享给
其他玩家；每台电脑应使用独立邀请领取自己的身份。

关闭主窗口或选择托盘“退出”会停止 VPN，并结束后台。遇到连接问题时，在退出前
点击“导出日志”，获取最近的有界、脱敏客户端与后台日志。后台日志随本次运行的
受保护工作目录清理。

程序使用随包的固定版本 OpenVPN 核心，TAP 驱动属于系统组件。退出后删除便携目录
即可移除程序和本地凭证；驱动与专用网卡会保留。需要移除专用网卡时，在设备管理器
中确认其 `EZGameLAN-` 名称后删除。使用其他 VPN 软件时应保留共享 TAP 驱动。

安装版用户应先使用原卸载器卸载旧版，停止其常驻 Service，再使用便携版和新邀请。
便携版通过粘贴加入链接使用。

在 Windows 仓库根目录生成便携包：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File ./packaging/client/build-release.ps1
```

构建需要 Go 1.22 或更新版本，以及仓库中的固定版本 OpenVPN MSI。脚本校验 MSI
后提取核心、DLL 和签名 TAP 驱动，不运行 MSI 安装。产物为上述 ZIP；WSL 或 Git
Bash 可通过 `./packaging/client/build-release.sh` 调用 Windows 构建脚本。

## 5. 管理端配置使用方法

构建 Windows x64 管理端：

```bash
./packaging/admin/build-admin.sh
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
- 查看在线设备、虚拟 IP、连接时间、每台设备流量和当前总流量；
- 生成并复制一次性加入链接；
- 断开在线设备；
- 吊销设备并立即断开连接，吊销后从设备列表移除；
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
│   ├── ezgamelan-backend/     Windows 按需提权后台
│   ├── ezgamelan-client/      Windows 玩家界面
│   └── ezgamelan-admin/       Windows 管理端启动器
├── internal/
│   ├── admin/、adminweb/      服务端管理协议和本机 WebUI
│   ├── client/、ipc/          客户端状态机和 Named Pipe
│   ├── portable/             提权后台生命周期和依赖校验
│   ├── join/、netcfg/         加入流程和 TAP 网卡配置
│   ├── config/、store/        服务端配置和文件状态
│   ├── ezlan/、enroll/        设备管理和一次性领取服务
│   ├── invite/、pki/          邀请格式和证书生命周期
│   ├── openvpn/               OpenVPN 配置、状态和管理通道
│   └── atomicfile/、logging/   原子写入和有界日志
├── packaging/
│   ├── server/                Linux 服务端部署、systemd 和 Nginx
│   ├── client/                Windows 玩家端便携包构建
│   └── admin/                 Windows 管理端构建
├── third_party/
│   └── openvpn/               固定版本 OpenVPN Windows MSI 和校验信息
├── go.mod、go.sum             Go 版本和锁定依赖
└── README.md                  部署与使用说明
```

构建产物统一写入被 Git 忽略的 `dist/`，运行配置、证书、邀请、日志和诊断文件
也不进入仓库。

## 7. 简要架构说明

```text
Windows 玩家电脑
  EZGameLAN.exe
          │ Named Pipe
          ▼
  EZGameLAN-Backend.exe ── openvpn.exe + TAP-Windows6
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
  `EZGameLAN-Backend.exe` 按需通过 UAC 启动，管理 OpenVPN、本机凭证和专用 TAP
  网卡。管道限定启动用户并核对双方进程身份；用户可写目录中的连接指令不会直接
  交给提权进程，后台从校验后的凭证材料生成固定配置；
- **加入流程：** 一次性 Token 通过 Nginx 的固定 HTTPS 路径领取设备配置，
  加入服务仅监听服务器回环地址，不接管现有静态站点；
- **管理流程：** WebUI 只在管理员电脑上按需监听回环地址，所有管理操作经系统
  SSH 和固定的 `ezlanctl admin-stdio` 命令执行；
- **服务端状态：** 配置、设备记录、证书和 CRL 使用本地文件保存，不引入数据库
  或公网管理 API；
- **运行恢复：** Linux 服务由 systemd 管理，Windows 后台随客户端运行，客户端
  退出后停止连接。连接状态持续读取 OpenVPN 管理接口，并核对专用网卡的协商地址；
  状态轮询、重连、日志和诊断均设置上限。
