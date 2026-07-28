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

`init` 会生成本地 CA、OpenVPN 服务端配置、`tls-crypt-v2` 密钥和加入服务
TLS 身份。`invite` 默认生成 30 分钟有效的一次性加入链接，也可通过
`--file PATH` 生成离线 `.ezlan`。

Windows 端计划复用官方 OpenVPN Community、OpenVPN GUI 和 TAP-Windows6，
项目仅提供按需运行的加入工具；该工具尚在后续实现范围。

开发检查：

```text
gofmt -w ./cmd ./internal
go test ./...
go vet ./...
```

架构和开发约束分别见 [DESIGN.md](DESIGN.md) 与 [AGENTS.md](AGENTS.md)。
