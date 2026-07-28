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

Windows 端计划复用官方 OpenVPN Community、OpenVPN GUI 和 TAP-Windows6，
项目仅提供按需运行的加入工具；该工具尚在后续实现范围。

开发检查：

```text
gofmt -w ./cmd ./internal
go test ./...
go vet ./...
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
