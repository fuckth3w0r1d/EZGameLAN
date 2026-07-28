# EZGameLAN 开发约定

本文档适用于整个仓库。开始修改前先读 `DESIGN.md`。

## 1. 项目目标

这是个人和朋友使用的小工具，规模最多 10 台客户端。取舍顺序：

1. 游戏能稳定联机；
2. 玩家操作简单；
3. 凭据和网络边界清楚；
4. 代码少、依赖少、容易维护；
5. 出错时容易定位和恢复；
6. 性能足够当前规模。

不要为未来规模引入分布式系统、数据库、消息队列、微服务、插件框架或复杂
缓存。相同需求真正出现后再抽象。

## 2. 当前架构边界

首版保持：

- OpenVPN TAP 是唯一数据面；
- 所有客户端流量经过一台 Linux 服务器；
- 单个虚拟 LAN，最多 10 台客户端；
- 默认网段 `10.86.0.0/24`；
- OpenVPN 使用 `21111/UDP`；
- Nginx 复用 `443/TCP` 提供公网加入 URL；
- 加入服务只监听 `127.0.0.1:21112/TCP`；
- Linux 使用 systemd；
- 服务端管理使用同一个 Go 程序 `ezlanctl`；
- Windows 复用官方 OpenVPN GUI、官方 Service 和 TAP-Windows6；
- 自有 Windows 程序只有按需运行的 `ezgamelan-join.exe`；
- 默认通过 `ezgamelan://` 一次性链接加入；
- `.ezlan` 固定 JSON 作为离线导入格式；
- 客户端不修改默认路由、DNS、IP forwarding 和物理网卡桥接；
- 虚拟 IP 直连是正式路径，LAN 自动发现是尽力支持；
- 文件状态代替数据库。

改动数据面、增加多房间/P2P/账号/数据库/Web UI、新平台或新的特权常驻进程
前，先更新 `DESIGN.md` 并写清当前需求。

## 3. 保持实现直接

- 只实现当前流程会调用的代码；
- 标准库够用时不增加依赖；
- 不创建空包和“未来可能使用”的接口；
- 小函数和直接数据结构优于框架；
- 同一逻辑出现三次前不急于抽象；
- 不为测试替换而预建多层 repository/service/controller；
- 配置只暴露玩家或管理员真正需要修改的字段；
- 依赖 OpenVPN 的能力时，优先调用和验证，不重复实现其状态机。

建议目录按需要创建：

```text
cmd/ezlanctl/          Linux 管理命令和 serve 子命令
cmd/ezgamelan-join/    Windows 一次性加入工具
internal/invite/       Token 和 .ezlan
internal/openvpn/      固定配置生成
internal/pki/          最小证书操作
packaging/             systemd 和 Windows 安装
tests/                 单元测试和显式集成测试
```

不要仅为了符合目录图创建占位文件。

## 4. Go 与脚本

自有程序优先使用 Go，版本以根目录 `go.mod` 为准。

- 提交前运行 `gofmt`、`go test ./...` 和 `go vet ./...`；
- 错误使用 `%w` 保留根因，库代码不调用 `os.Exit` 或 `log.Fatal`；
- 可取消或可能阻塞的操作接收 `context.Context`；
- goroutine 必须有所有者、退出条件和等待路径；
- 文件和句柄及时关闭；
- 外部程序使用 `exec.CommandContext` 和独立参数，不通过 shell 拼接；
- stdout/stderr 和网络响应设置大小上限；
- Windows 专用代码放在 `_windows.go` 文件；
- 修改 Windows 代码后至少完成 Windows x64 交叉构建。

Shell 使用 `#!/usr/bin/env bash`、`set -euo pipefail` 和正确引用的变量。安装脚本
应可重复运行，只清理带 EZGameLAN 明确标识的资源。

## 5. 加入链接和秘密

### 5.1 Token

- 使用密码学随机源，至少 128 位；
- 默认 30 分钟过期；
- 服务端只保存 Token 哈希；
- Token 放在请求体中，不放入 HTTP 路径或查询参数；
- 成功领取后删除待领取配置；
- 日志只记录请求结果和脱敏客户端名称；
- 加入服务不读取 CA 私钥，只读取已经生成的待领取配置。

### 5.2 `.ezlan`

- UTF-8 固定 JSON，拒绝未知和重复字段；
- 总大小上限 `256 KiB`；
- 只包含版本、客户端名称、endpoint、服务端名称和固定 PEM 字段；
- 校验 endpoint、PEM 类型、证书链、有效期和证书/私钥匹配；
- 使用内置模板生成 `.ovpn`，不接受用户提供的 OpenVPN 指令；
- 私钥、Token、完整链接和完整 `.ezlan` 不进入日志、错误或测试快照；
- 测试材料只能使用 `tests/fixtures` 中的专用凭据。

### 5.3 文件

- Linux CA 私钥只允许 root 读取；
- 待领取目录只允许管理员和加入服务读取；
- Windows 配置只允许当前用户、Administrators 和 `SYSTEM` 读取；
- 权限设置失败时终止导入或生成；
- 配置和 CRL 使用临时文件、`fsync` 和原子替换；
- 邀请输出拒绝符号链接并默认拒绝覆盖。

## 6. 网络约束

- 显式使用 `dev tap` 和 `windows-driver tap-windows6`；
- 服务端使用 `client-to-client` 和 `max-clients 10`；
- 公网物理网卡不加入 TAP 或 bridge；
- 客户端不接收默认路由和 DNS；
- OpenVPN 拥有虚拟 IP 和 MTU；
- 首版省去 Windows 防火墙管理代码，依赖 Windows 和游戏的程序规则；
- 同一虚拟 LAN 的朋友视为受信成员；
- 发现本地网段冲突时给出明确错误，不覆盖现有路由；
- 不解析、代理、复制或记录游戏数据包。

## 7. 服务端状态

服务端只维护配置、PKI、CRL、客户端记录和短期待领取配置。

- `ezlanctl init` 幂等，不覆盖现有 CA；
- 每台设备使用独立证书，禁用 `duplicate-cn`；
- `invite` 默认拒绝同名有效设备；
- `revoke` 幂等并原子更新 CRL；
- `serve` 仅暴露领取接口，远程管理保持关闭；
- Token 过期后删除待领取私钥和配置，并允许该名称重新生成邀请；
- systemd 负责 OpenVPN 和加入服务重启。

## 8. 错误与日志

错误分类保持少量：

```text
E_JOIN_*       链接、Token 和配置导入
E_CONNECT_*    OpenVPN、TAP 和虚拟网络
E_INSTALL_*    安装、升级和卸载
E_INTERNAL_*   其他内部错误
```

UI 信息使用简体中文并说明下一步。日志默认 INFO、有大小和保留数量上限；连接
状态变化记录一次，高频保活和数据包不逐条记录。

## 9. 测试

默认测试不得修改宿主网络。至少覆盖：

- Token 生成、过期、重复领取和哈希比较；
- `.ezlan` 严格 JSON、大小上限和 PEM 校验；
- 固定 OpenVPN 配置生成；
- 原子文件更新和权限失败；
- 同名邀请、吊销和 CRL 幂等；
- 日志脱敏；
- Windows URL 解析和网段冲突检测。

需要 OpenVPN、TAP、管理员权限或真实网络的测试放入 `tests/integration`，通过
单独命令运行并清理创建的进程、配置和适配器。

发布给朋友前手工验证：

- Windows 10/11 安装和卸载；
- 点击链接到连接成功的完整流程；
- Minecraft Java Edition 和 L4D2 虚拟 IP 联机；
- 10 客户端上限；
- 断网、换网、服务端重启；
- 证书吊销。

## 10. 变更纪律

- 改动保持聚焦，不顺带重构无关代码；
- 不覆盖用户已有修改；
- 不提交真实私钥、Token、邀请文件、日志和安装产物；
- 用户行为或网络边界变化同步更新 `DESIGN.md`；
- 修复缺陷时优先增加能复现问题的测试；
- 删除功能时同步删除失效配置、测试和文档；
- 新依赖简短记录用途、体积影响、许可证和移除路径。

完成一项功能的标准是：真实流程可用、失败提示清楚、秘密不进日志、相关测试
通过、安装或网络变更有对应清理。除此之外不增加额外流程和文档负担。
