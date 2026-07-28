# EZGameLAN 简化架构

> 面向个人和朋友联机，单个虚拟局域网，最多 10 台 Windows 客户端。

## 1. 先把事情做简单

EZGameLAN 只解决一件事：让几位朋友像在同一局域网中一样联机。

首版优先级：

1. 玩家安装一次，以后点加入链接就能连接；
2. Minecraft Java Edition、L4D2 等游戏能稳定直连；
3. 断网后由 OpenVPN 自己重连；
4. 服务端和客户端尽量复用现成组件；
5. 出问题时能看懂日志并重新加入。

LAN 自动发现尽量支持，但虚拟 IP 直连是可靠路径。首版不追求账号、房间大厅、
成员列表、聊天、P2P、Web 管理后台、自动更新和复杂诊断。

## 2. 最小架构

```text
Linux 服务器
├── OpenVPN Server                    UDP/21111
├── ezlanctl serve（加入配置下载）    TCP/21112
└── ezlanctl（init/invite/revoke）

Windows 玩家电脑
├── 官方 OpenVPN Community
│   ├── OpenVPN GUI / 官方 Service
│   └── TAP-Windows6
└── ezgamelan-join.exe（按需运行）
```

核心取舍：

- 数据面仍使用 OpenVPN TAP，不开发隧道协议；
- 所有客户端流量经服务器转发；
- 客户端复用官方 OpenVPN GUI、Service 和重连能力；
- 项目只写一个很小的 Windows 加入工具，省去自有托盘 UI、Windows Service、
  Named Pipe、网络状态机和防火墙管理器；
- 服务端只有 OpenVPN 和一个很小的加入服务常驻；
- 加入服务与管理 CLI 使用同一个 Go 可执行文件 `ezlanctl`；
- 状态放在文件中，不使用数据库。

这套结构比自研 Service + UI 少很多 Windows 特权代码，也更容易测试和维护。

## 3. 玩家如何加入

管理员在服务器执行：

```text
ezlanctl invite alice
```

命令输出一条加入链接：

```text
ezgamelan://join?server=HOST:21112&pin=SERVER_PIN#TOKEN
```

玩家流程：

```text
第一次安装 EZGameLAN
→ 点击朋友发来的加入链接
→ 加入工具下载设备配置
→ 自动写入 OpenVPN GUI 的用户配置目录
→ 发起连接
→ 显示“已连接”和自己的虚拟 IP
→ 启动游戏
```

之后连接和断开直接使用官方 OpenVPN GUI 托盘菜单。加入工具也允许粘贴链接，
避免聊天软件拦截自定义 URI。

管理员还可以生成离线文件：

```text
ezlanctl invite alice --file alice.ezlan
```

玩家双击 `.ezlan`，由同一个加入工具导入。链接是默认方式，文件是离线兜底，
两条路径最终使用同一种设备配置数据。

## 4. 加入链接和 Token

单独输入服务器 IP 只解决“连接哪台服务器”，还缺少客户端证书、私钥和服务端
身份。因此首版采用“服务器地址 + 一次性 Token + 服务器公钥指纹”。

### 4.1 生成

`ezlanctl invite alice`：

1. 为 alice 生成独立客户端证书和私钥；
2. 生成至少 128 位随机 Token，默认 30 分钟过期；
3. 把待领取的 `.ezlan` 放入受限目录；
4. 文件中只保存 Token 哈希，不保存明文 Token；
5. 输出包含地址、公钥指纹和 Token 的加入链接。

同名有效设备默认拒绝重复生成。重新发放时先吊销旧设备，再生成新链接。

### 4.2 领取

`ezgamelan-join.exe` 从链接读取服务器地址、公钥指纹和 Token，然后：

1. 连接 `https://HOST:21112/v1/enroll`；
2. 使用链接中的公钥指纹校验加入服务，不依赖公网域名和公共 CA；
3. 在 POST 请求体中发送 Token，避免 Token 进入访问日志；
4. 下载 `.ezlan`，完成大小和格式校验；
5. 服务端在响应发送完成后销毁待领取文件。

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

生成的 `.ovpn` 与私钥只允许当前用户、Administrators 和 `SYSTEM` 读取。导入
成功后，文件方式会提示删除源 `.ezlan`。

## 6. Windows 客户端

### 6.1 复用官方 OpenVPN

首版锁定一个经过测试的 OpenVPN Community Windows x64 版本，并明确安装
TAP-Windows6。官方组件负责：

- TAP 驱动；
- 特权 OpenVPN 进程；
- 托盘状态；
- 连接和断开；
- keepalive 与基础重连；
- 日志。

`ezgamelan-join.exe` 只负责：

- 处理 `ezgamelan://` 和 `.ezlan` 文件关联；
- 下载、校验和保存配置；
- 检查默认虚拟网段是否与本机接口/路由冲突；
- 调用官方 OpenVPN GUI 支持的连接入口；
- 等待虚拟 IP 出现并把结果显示给玩家。

如果官方 GUI 的自动连接接口在实测中不稳定，首版退回为“配置已经导入，请在
托盘点击连接”的单步提示，而不是新增自有 Service。

### 6.2 活动配置

首版只保留一个 `ezgamelan.ovpn`：

- 新配置完整校验后再原子替换；
- 替换前保留一份本地备份；
- 新连接失败时允许一键恢复上一份；
- 卸载时删除本机配置和私钥；
- 本机卸载不会吊销服务器证书，设备退出时仍要由管理员执行 `revoke`。

### 6.3 Windows 防火墙

项目不创建宽泛的虚拟网段放行规则，也不管理 SMB、RPC、RDP 等系统规则。
TAP 保持 Windows 默认网络配置，由 Windows 防火墙和游戏自己的程序规则处理
入站流量。

玩家作为主机时，如果 Windows 弹出游戏网络访问提示，应允许对应游戏。连接
失败时加入工具提示检查该游戏的 Windows 防火墙规则并使用虚拟 IP 直连。

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
| 加入服务 | `21112/TCP` |
| TAP MTU 起始值 | `1400` |

OpenVPN 分配 IP 和 MTU，项目代码只检查结果。客户端不接管默认路由、不修改
DNS、不启用 IP forwarding，也不桥接物理网卡。

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
```

职责：

- `init` 创建配置、CA、服务端证书、加入服务 TLS 身份和 systemd unit；
- `serve` 提供唯一的 `/v1/enroll` 接口；
- `invite` 生成客户端凭据、Token 和链接或离线文件；
- `revoke` 更新 CRL；已有连接会在重新认证时失效，需要立即断开时由管理员
  重启 OpenVPN；
- `list/status` 查看少量必要状态。

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

服务器只开放 `21111/UDP` 和 `21112/TCP`，加入服务接口之外没有管理 API。

## 9. 安全边界

这是朋友之间的虚拟局域网，不做成员间隔离。加入 LAN 的人可向其他成员发送
二层和 IP 流量，因此邀请只发给信任的人。

仍需坚持几条底线：

- 每台设备独立证书，丢失或退出时可单独吊销；
- 禁用 `duplicate-cn`、压缩、外部脚本和插件；
- 客户端固定校验服务端证书身份；
- Token 随机、短期、使用后删除，服务端只保存哈希；
- 私钥、Token 和完整加入链接不进入日志；
- OpenVPN、TAP 驱动和安装包锁定版本与来源；
- 所有进程启动使用独立参数，不经过 shell 拼接；
- 卸载和清理只操作带 EZGameLAN 标识的文件与配置。

不增加账号、密码找回、验证码、邮件、数据库和管理后台。

## 10. 实现顺序

### 第一步：手工验证 OpenVPN TAP

先用官方 OpenVPN 客户端和两台真实 Windows 电脑验证：

- 两台机器互相 Ping、TCP/UDP；
- Minecraft Java Edition 和 L4D2 虚拟 IP 直连；
- 广播、多播和 LAN 自动发现的实际表现；
- 断网、换网和服务端重启后的恢复；
- MTU 1400 是否稳定；
- 10 个客户端能加入，第 11 个被拒绝。

手工验证通过后再写加入服务和 Windows 加入工具。

### 第二步：服务端工具

实现 `init`、`invite`、`serve`、`revoke`、`list` 和 `status`，先用命令行完成
链接领取和吊销闭环。

### 第三步：Windows 加入工具

实现 URI/文件导入、TLS 指纹校验、配置生成、OpenVPN GUI 连接和虚拟 IP 显示。
界面只需要一个进度窗口和明确错误提示。

### 第四步：打包

做一个简单安装器，安装或检查官方 OpenVPN、注册 URI 与文件关联。升级和
卸载只验证当前真实使用路径，不预建复杂更新系统。

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
- Web/Electron UI；
- Linux/macOS/手机客户端；
- 自动更新和远程诊断；
- 游戏端口插件；
- 自研数据面；
- 通用事务、工作流或插件框架。

出现真实需求时再更新本文档，而不是提前保留实现路径。
