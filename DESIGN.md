# EZGameLAN 简化架构

> 面向个人和朋友联机，单个虚拟局域网，最多 10 台 Windows 客户端。

## 1. 先把事情做简单

EZGameLAN 只解决一件事：让几位朋友像在同一局域网中一样联机。

首版优先级：

1. 玩家安装一次，以后点加入链接就能连接；
2. Minecraft Java Edition、L4D2 等游戏能稳定直连；
3. 断网或进程异常后自动重连；
4. 服务端和客户端尽量复用现成组件；
5. 出问题时能看懂日志并重新加入。

LAN 自动发现尽量支持，但虚拟 IP 直连是可靠路径。首版不追求账号、房间大厅、
聊天、P2P、服务器公网 Web 管理后台、自动更新和复杂诊断。

## 2. 最小架构

```text
Linux 服务器
├── OpenVPN Server                         UDP/21111
├── Nginx（复用已有 HTTPS 站点）            TCP/443
├── ezlanctl serve（加入配置下载）          127.0.0.1:21112
└── ezlanctl（init/invite/revoke/admin-stdio）

Windows 玩家电脑
├── ezgamelan-client.exe（普通用户界面）
├── EZGameLAN Service（唯一自有特权进程）
├── openvpn.exe（由 Service 管理）
└── TAP-Windows6

管理员电脑
└── EZGameLAN-Admin.exe
    ├── 临时本机 WebUI                     127.0.0.1:随机端口
    └── 系统 ssh → sudo ezlanctl admin-stdio
```

核心取舍：

- 数据面仍使用 OpenVPN TAP，不开发隧道协议；
- 所有客户端流量经服务器转发；
- Windows 分发包复用 OpenVPN 核心和 TAP-Windows6，不安装或依赖 OpenVPN
  GUI；
- 自有 Windows Service 只管理一份配置和一个 OpenVPN 子进程；
- 普通用户客户端通过受限 Named Pipe 请求加入、连接、断开和查看状态；
- 服务端只有 OpenVPN、已有 Nginx 和一个很小的加入服务常驻；
- 加入服务与管理 CLI 使用同一个 Go 可执行文件 `ezlanctl`；
- 管理界面只在管理员电脑上按需运行，不在服务器开放管理端口；
- 状态放在文件中，不使用数据库。

增加自有 Service 的直接原因是让玩家不接触 OpenVPN GUI、配置目录和管理员
操作。Service 不实现隧道协议，不代理游戏数据，也不扩展为通用网络管理框架。

## 3. 玩家如何加入

管理员在服务器执行：

```text
ezlanctl invite alice
```

命令输出一条加入链接：

```text
ezgamelan://join?url=https%3A%2F%2FHOST%2Fezgamelan%2Fv1%2Fenroll#TOKEN
```

玩家流程：

```text
第一次安装 EZGameLAN
→ 打开客户端
→ 在一个输入框粘贴完整加入链接（也兼容 URL + Token）
→ 点击“加入并连接”
→ 显示“已连接”和自己的虚拟 IP
→ 启动游戏
```

之后只使用 EZGameLAN 客户端的“连接”和“断开”。高级 OpenVPN 参数不展示给
玩家。

管理员还可以生成离线文件：

```text
ezlanctl invite alice --file alice.ezlan
```

玩家双击 `.ezlan`，由同一个客户端导入。链接是默认方式，文件是离线兜底，
两条路径最终使用同一种设备配置数据。

## 4. 加入 URL 和 Token

单独输入服务器 IP 只解决“连接哪台服务器”，还缺少客户端证书、私钥和服务端
身份。因此首版采用“HTTPS URL + 一次性 Token”。公网 TLS 直接复用已有
Nginx 和受信任证书，玩家不必输入端口、证书指纹或更多参数。

### 4.1 生成

`ezlanctl invite alice`：

1. 为 alice 生成独立客户端证书和私钥；
2. 生成至少 128 位随机 Token，默认 30 分钟过期；
3. 把待领取的 `.ezlan` 放入受限目录；
4. 文件中只保存 Token 哈希，不保存明文 Token；
5. 输出包含 HTTPS 加入 URL 和 Token 的加入链接。

同名有效设备默认拒绝重复生成。重新发放时先吊销旧设备，再生成新链接。

### 4.2 领取

EZGameLAN Service 接收客户端提交的 HTTPS URL 和 Token，然后：

1. 连接 `https://HOST/ezgamelan/v1/enroll`；
2. 使用系统证书库正常校验 Nginx 的 HTTPS 证书；
3. 在 POST 请求体中发送 Token，避免 Token 进入 Nginx 访问日志；
4. 下载 `.ezlan`，完成大小和格式校验；
5. 服务端在响应发送完成后销毁待领取文件。

Nginx 只为这个精确路径增加反向代理，静态网页的 `location /` 保持原样。Go
加入服务只提供本机 HTTP，不直接暴露公网端口，也不持有公网 TLS 私钥。

Token 过期、已使用或校验失败时，管理员重新生成一条链接即可。加入服务只读取
已经生成的待领取配置，无权读取 CA 私钥，也没有在线签发能力。

链接本身就是临时凭据，应单独发给对应朋友。Token 使用 `#TOKEN` 放在 URI
fragment 中，减少浏览器和 HTTP 日志记录；客户端不会把完整链接写入日志。

## 5. `.ezlan` 格式

`.ezlan` 使用固定 JSON，而不是解析任意 OpenVPN 配置：

```json
{
  "format_version": 1,
  "client_name": "alice",
  "endpoint": "HOST:21111",
  "server_name": "ezgamelan-server",
  "ca_pem": "...",
  "cert_pem": "...",
  "key_pem": "...",
  "tls_crypt_v2_pem": "..."
}
```

简单约束：

- UTF-8，总大小不超过 256 KiB；
- 未知字段、重复字段、空字段和多余内容直接报错；
- 客户端名称只允许字母、数字、短横线和下划线，最长 32 个字符；
- endpoint 分开校验主机和端口；
- PEM 类型、数量、证书链、有效期及证书/私钥匹配关系都要检查；
- 日志和错误中不出现 `key_pem`、Token 或完整配置。

加入工具使用内置模板生成最终 `.ovpn`。模板固定 `dev tap`、
`windows-driver tap-windows6`、服务端证书名称校验、禁用压缩，并排除默认路由、
DNS、脚本、插件和外部文件引用。

生成的 `.ovpn` 与私钥只允许 Administrators 和 `SYSTEM` 读取。普通客户端只
能通过 Named Pipe 请求状态和操作，读取不到私钥。导入成功后，文件方式会提示
删除源 `.ezlan`。

## 6. Windows 客户端

### 6.1 最小进程划分

首版分发锁定 OpenVPN Community 2.6.21 Windows x64，并明确安装 TAP-Windows6，
但不分发 OpenVPN GUI。升级版本前重新验证 TAP、重连、MTU 和卸载。

`ezgamelan-service.exe` 作为 Windows Service 运行，负责：

- 领取、校验服务端返回的 `.ezlan` 并原子保存唯一活动配置；
- 启动、停止并等待一个受控的 `openvpn.exe`；
- OpenVPN 异常退出时进行有上限的重启；
- 检查 TAP 虚拟 IP，返回连接状态；
- 将已连接的 TAP 友好名称设为 `EZGameLAN`；
- 保存有界、脱敏日志并通过 IPC 提供受限诊断快照。

`ezgamelan-client.exe` 以普通用户运行，只提供：

- 一个加入信息输入框，接受完整 `ezgamelan://` 链接或用空白分隔的 URL 与
  Token；
- 加入、连接、断开和导出日志按钮；
- 每 2 秒读取一次连接状态，显示虚拟 IP 和虚拟网卡名称；
- 关闭主窗口后保留托盘图标，托盘菜单只有打开和退出；
- 清晰的中文错误和下一步。

离线 `.ezlan` 导入复用同一个 Service 校验与安装路径，在 URL + Token 主流程
完成真机验证后再接入文件选择按钮。

客户端和 Service 使用固定版本的本机 Named Pipe JSON 协议。普通请求上限为
16 KiB，诊断响应上限为 128 KiB；Pipe ACL 只允许本机已登录用户、
Administrators 和 `SYSTEM`；每次请求由 Service 再次校验字段。首版不做
多用户会话、多配置或远程控制。

Service 日志位于 `%ProgramData%\EZGameLAN\logs`，单文件最多 256 KiB，只
保留当前文件和两份滚动备份，总上限约 768 KiB。日志记录状态变化、稳定错误码
和 OpenVPN 最近输出，不记录 Token、完整加入链接或配置内容。“导出日志”
由 Service 先截取最近 128 KiB 并再次脱敏，再交给普通用户客户端选择保存位置。
普通 UI 日志位于 `%LocalAppData%\EZGameLAN\logs`，单文件最多 128 KiB，
只保留一份备份；导出时附带其中最近 32 KiB。两类日志总保留量约 1 MiB。

### 6.2 活动配置

首版只保留一个 `ezgamelan.ovpn`：

- 新配置完整校验后再原子替换；
- 替换前保留一份本地备份；
- 配置保存在 `%ProgramData%\EZGameLAN\config`；
- 卸载时删除本机配置和私钥；
- 本机卸载不会吊销服务器证书，设备退出时仍要由管理员执行 `revoke`。

Service 只在同时确认 `10.86.0.10`～`10.86.0.19` 虚拟地址和 TAP 名称或
硬件地址特征后重命名对应网卡，不会按模糊名称操作其他网卡。首次重命名前在
受保护配置目录记录原名称；升级保留该记录，卸载时恢复原名称，然后再按安装
来源决定是否卸载 OpenVPN。

### 6.3 Windows 防火墙

项目不创建宽泛的虚拟网段放行规则，也不管理 SMB、RPC、RDP 等系统规则。
TAP 保持 Windows 默认网络配置，由 Windows 防火墙和游戏自己的程序规则处理
入站流量。

玩家作为主机时，如果 Windows 弹出游戏网络访问提示，应允许对应游戏。连接
失败时客户端提示检查该游戏的 Windows 防火墙规则并使用虚拟 IP 直连。

## 7. 服务端网络

首版优先使用最简单的服务端配置：

```text
OpenVPN dev tap
+ client-to-client
+ max-clients 10
+ 10.86.0.0/24
+ 不连接任何物理网卡
```

默认参数：

| 参数 | 默认值 |
|---|---|
| 虚拟网段 | `10.86.0.0/24` |
| 服务端虚拟 IP | `10.86.0.1` |
| 客户端地址 | `10.86.0.10`～`10.86.0.19` |
| OpenVPN | `21111/UDP` |
| 公网加入 URL | `443/TCP` 的 `/ezgamelan/v1/enroll` |
| 本机加入服务 | `127.0.0.1:21112/TCP` |
| TAP MTU 起始值 | `1400` |

OpenVPN 分配 IP 和 MTU，项目代码只检查结果。客户端不接管默认路由、不修改
DNS、不启用 IP forwarding，也不桥接物理网卡。

OpenVPN 每 5 秒将机器可读的 `status-version 3` 状态写入
`/run/ezgamelan/openvpn-status.log`，并在权限为 `0700` 的运行目录中创建
Unix management socket。状态文件用于显示当前在线设备、虚拟地址、连接时间
和字节计数；management socket 只用于查询状态和按 Client ID 断开连接。两者
都不对公网监听，服务器重启后由 systemd 的 `RuntimeDirectory` 重新创建。

纯 TAP 在手工测试中满足互通、广播、多播和游戏直连后就固定下来。若它连基本
游戏通信都未通过，再回到数据面选型，不在首版堆叠 Linux bridge 和额外网络
脚本。

TAP 可能承载 Windows 产生的 IPv6 链路本地流量。首版不依赖 IPv6，也不编写
禁用 IPv6 绑定的代码；同一虚拟 LAN 中的朋友按受信局域网成员看待。

## 8. 服务端管理

`ezlanctl` 只保留几个命令：

```text
ezlanctl init
ezlanctl serve
ezlanctl invite <name> [--file PATH]
ezlanctl revoke <name>
ezlanctl list
ezlanctl status
ezlanctl admin-stdio
```

职责：

- `init` 创建配置、CA、服务端证书和 OpenVPN 配置；
- `serve` 提供唯一的 `/v1/enroll` 接口；
- `invite` 生成客户端凭据、Token 和链接或离线文件；
- `revoke` 更新 CRL；
- `list/status` 查看少量必要状态。
- `admin-stdio` 通过标准输入输出提供固定、版本化的管理消息，仅供 SSH 调用。

### 8.1 管理界面

管理员使用单独分发的 `EZGameLAN-Admin.exe`。程序不预设具体 SSH 主机。每次
启动先显示一个原生小窗口，要求输入 SSH 配置别名或 `USER@HOST`；连接目标经过
校验后，程序先执行一次 `snapshot` 请求确认 SSH、sudoers 和管理协议均可用。
连接成功才监听 `127.0.0.1` 的随机端口、生成随机会话路径并打开系统浏览器。
最近一次验证成功的 SSH 目标保存在当前用户配置目录，下次启动时预填，连接失败
可直接修改后重试。

HTML、CSS 和 JavaScript 直接嵌入 Go 可执行文件，不使用 Electron、Node.js 或
外部 Web 资源。WebUI 绑定启动时已验证的单一 SSH 目标，页面只显示当前服务器，
不再接收或切换 SSH 主机参数。关闭页面后，进程在短暂空闲超时后自行退出。

管理界面提供当前实际需要的功能：

- 查看 OpenVPN 和加入服务健康状态；
- 查看设备的证书状态、在线状态、虚拟 IP、连接时间和收发字节数；
- 创建一次性加入链接并复制；
- 断开在线设备；
- 吊销设备并立即断开其现有连接；
- 只重启 EZGameLAN 的 OpenVPN 或加入服务；
- 查看并导出有界、脱敏的最近诊断信息。

管理员在启动窗口填写 SSH 配置别名或 `USER@HOST`。程序使用系统 `ssh` 客户端
和用户已有的 SSH 配置、密钥或 agent，每个操作执行固定参数的命令：

```text
ssh -T -o BatchMode=yes HOST_ALIAS sudo -n /usr/local/bin/ezlanctl admin-stdio
```

请求和响应是单行 JSON，包含固定 `protocol_version` 和白名单动作；单个请求
和响应都有大小、超时和并发上限。服务器端拒绝未知字段和未知动作。主机别名
作为独立参数并经过长度和字符集校验，设备名仍使用既有严格校验；任何用户输入
都不拼接进远端 shell 命令。

部署时只为指定 SSH 用户安装以下精确 sudo 规则：

```text
USER ALL=(root) NOPASSWD: /usr/local/bin/ezlanctl admin-stdio
```

`admin-stdio` 是按请求启动的短进程，不是常驻 API。它只允许上述界面需要的
固定操作，重启目标也只允许两个 EZGameLAN systemd unit。服务器不新增公网
WebUI、管理监听端口、账号系统或数据库，已有 Nginx 静态网页和
`location /` 保持不变。

本机 WebUI 绑定回环地址，并同时校验随机会话路径、`Host` 和 `Origin`；响应
使用禁止缓存和限制脚本来源的头部。加入链接只显示在当前页面，不写入日志和
浏览器持久存储。保存的 SSH 目标只含别名或主机名，不含私钥、口令和 Token。
SSH 失败时在启动窗口显示原因，提示管理员先在终端完成主机指纹确认和密钥
登录。

### 8.2 扩展边界

管理 JSON 协议通过版本号和能力列表演进。一个管理程序可切换多个本机 SSH
别名，但当前仍坚持“一台服务器一个虚拟 LAN”。以后确有多个独立 LAN 的需求
时，优先增加多个服务器配置，而不是先在一个 OpenVPN 实例中引入房间、租户、
数据库和权限模型。

文件布局保持简单：

```text
/etc/ezgamelan/           配置、CA、服务端密钥、CRL
/var/lib/ezgamelan/       客户端记录和待领取配置
/var/log/ezgamelan/       有界日志
```

CA 私钥只允许管理员命令读取；加入服务只能读取待领取目录。写配置、CRL 和
待领取文件时使用临时文件、`fsync` 和原子替换。邀请输出默认不覆盖已有文件。
证书和 CRL 使用适合个人部署的较长有限有效期，`status` 在到期前给出续签或
刷新提示。

服务器对公网开放 `21111/UDP`，并复用已有 Nginx 的 `443/TCP`。`21112/TCP`
只绑定回环地址，加入接口之外没有常驻管理 API。OpenVPN management socket
和状态文件只存在于 root 可访问的 `/run/ezgamelan`。Nginx 的静态站点和已有
路径不由 EZGameLAN 安装脚本接管。

## 9. 安全边界

这是朋友之间的虚拟局域网，不做成员间隔离。加入 LAN 的人可向其他成员发送
二层和 IP 流量，因此邀请只发给信任的人。

仍需坚持几条底线：

- 每台设备独立证书，丢失或退出时可单独吊销；
- 禁用 `duplicate-cn`、压缩、外部脚本和插件；
- 客户端使用系统证书库校验加入 URL 的 HTTPS 身份，并固定校验 OpenVPN
  服务端证书身份；
- Token 随机、短期、使用后删除，服务端只保存哈希；
- 私钥、Token 和完整加入链接不进入日志；
- 管理权限只通过 SSH 密钥和精确的 `sudoers` 命令授予，不把管理凭据存入
  EZGameLAN；
- OpenVPN、TAP 驱动和安装包锁定版本与来源；
- 所有进程启动使用独立参数，不经过 shell 拼接；
- 卸载和清理只操作带 EZGameLAN 标识的文件与配置。

不增加账号、密码找回、验证码、邮件、数据库和管理后台。

## 10. 实现顺序

### 第一步：服务端闭环

实现 `init`、`invite`、`serve`、`revoke`、`list` 和 `status`，完成链接领取、
吊销和 Nginx 精确路径代理。

### 第二步：Windows 客户端骨架

实现 URL + Token 输入、Named Pipe、配置生成、OpenVPN 子进程管理和虚拟 IP
显示。界面只保留当前流程需要的控件。

### 第三步：Windows 真机验证

使用两台真实 Windows 电脑验证：

- 两台机器互相 Ping、TCP/UDP；
- Minecraft Java Edition 和 L4D2 虚拟 IP 直连；
- 广播、多播和 LAN 自动发现的实际表现；
- 断网、换网和服务端重启后的恢复；
- MTU 1400 是否稳定；
- 10 个客户端能加入，第 11 个被拒绝。

### 第四步：打包

做一个简单安装器，安装固定版本的 OpenVPN 核心、TAP-Windows6、Service 和
客户端，并注册 URI。最终只分发一个 `EZGameLAN-Setup.exe`：安装器内嵌经过
固定 SHA-256 和 Authenticode 签名校验的官方 OpenVPN MSI，只选择核心与
TAP-Windows6 组件。玩家双击 EXE 并确认一次 UAC；随后显示原生阶段进度条和
当前步骤，包括校验安装包、安装 OpenVPN/TAP、复制程序、配置 Service 和启动
客户端。PowerShell 安装脚本只通过受限临时进度文件报告固定阶段与百分比，不向
界面传递任意文本。升级和卸载只验证当前真实使用路径。

### 第五步：轻量管理端

在服务端闭环和 Windows 玩家端完成真机连接后，增加 OpenVPN 机器可读状态、
本地 Unix management socket、`admin-stdio` 和单独的本机管理 WebUI。管理端
不装进玩家客户端，不改变数据面和 Nginx 公网边界。

## 11. 首版完成标准

满足这些实际结果即可发布给朋友使用：

- 新玩家安装一次后可通过加入链接连接；
- Minecraft Java Edition 和 L4D2 使用虚拟 IP 连续游戏 60 分钟；
- 10 个客户端同时在线；
- 普通断网和服务端重启后 OpenVPN 能恢复；
- Token 过期、重复使用和证书吊销结果明确；
- 网段冲突、OpenVPN 缺失和防火墙问题有中文提示；
- 日志中没有 Token、私钥和完整设备配置；
- 卸载后不留下项目创建的活动配置。

吞吐、内存和安装包体积以实测记录为主。只要 10 人游戏流畅，就不为漂亮的
数字引入额外进程、缓存、数据库或抽象层。

## 12. 暂不考虑

- 多房间和公网大厅；
- P2P 和打洞；
- 服务器公网 WebUI 和 Electron UI；
- Linux/macOS/手机客户端；
- 自动更新和远程诊断；
- 游戏端口插件；
- 自研数据面；
- 通用事务、工作流或插件框架。

出现真实需求时再更新本文档，而不是提前保留实现路径。

## 13. 自有依赖

`golang.org/x/sys/windows` 用于 Windows Service、Named Pipe ACL 和 Win32
界面。Go 标准库没有覆盖这些 API；该依赖只编译进两个可执行文件，不增加常驻
进程，也不引入动态运行时。版本锁定在 `go.mod` 和 `go.sum`。若后续 Go 标准库
覆盖所需能力，可逐项替换并移除。

管理端使用 Go 标准库的本机 HTTP、`embed`、进程执行和加密随机数能力，并调用
Windows 系统自带或管理员已安装的 OpenSSH 客户端。当前需求只涉及少量表格和
按钮，标准库与原生 HTML 已足够，因此不增加 Web 框架、前端工具链或新 Go
依赖。管理端按需运行，服务器只增加 OpenVPN 现有进程的状态文件和 Unix
socket；移除管理端时删除独立 EXE、精确 sudoers 文件和两条 OpenVPN 配置即可，
不影响玩家连接数据和设备记录。
