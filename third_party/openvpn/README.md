# OpenVPN Windows 构建依赖

EZGameLAN Windows 玩家端使用仓库内固定版本的 OpenVPN Community MSI，以便在
离线或全新环境中直接生成单文件安装包。

| 项目 | 值 |
|---|---|
| 文件 | `OpenVPN-2.6.21-I001-amd64.msi` |
| 版本 | OpenVPN Community 2.6.21-I001 Windows x64 |
| 来源 | <https://build.openvpn.net/downloads/releases/OpenVPN-2.6.21-I001-amd64.msi> |
| SHA-256 | `845b654edb92cc02a05dc335693980c4f2d4a4f09689d0844309753de2cb5471` |
| 许可证 | GPL-2.0，<https://github.com/OpenVPN/openvpn/blob/master/COPYING> |

校验文件：

```bash
cd third_party/openvpn
sha256sum -c SHA256SUMS
```

`packaging/client/build-release.sh` 和
`packaging/client/build-release.ps1` 在构建前校验固定文件名与 SHA-256；
PowerShell 构建流程还会校验 Authenticode 签名和签名者。

该文件增加约 5.7 MiB 仓库体积，不增加客户端安装体积、常驻内存或启动进程，
因为此前构建流程已经使用同一 MSI。升级或替换时同步更新本目录校验文件、
Windows 构建脚本、`internal/setup` 中的固定包名以及相关测试。
