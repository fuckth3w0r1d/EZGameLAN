# EZGameLAN

## 客户端使用

客户端支持 Windows 10/11 x64。完整解压 `EZGameLAN-Portable.zip` 到当前用户可写的本地目录，保留其中的 `EZGameLAN.exe`、`EZGameLAN-Backend.exe` 和 `openvpn/`。

1. 双击 `EZGameLAN.exe`。
2. 将管理员提供的完整加入链接粘贴到“添加局域网”输入框。
3. 点击“添加并连接”，按提示确认 UAC。程序会按需安装 TAP 驱动；如果系统要求重启，重启电脑后重新连接。
4. 显示“已连接”和虚拟 IP 后启动游戏。默认虚拟 IP 为 `10.86.0.x`，可在游戏中使用其他玩家的虚拟 IP 直连。

每台电脑使用独立邀请。加入成功后，凭证保存在“已保存的局域网”列表中，以后选择凭证并点击“连接所选”即可。同一时间只能连接一个局域网。

关闭主窗口或选择托盘“退出”会停止 VPN。连接失败时，可先重试；需要排查问题时，在退出前点击“导出日志”。

凭证和日志保存在解压目录的 `data/` 中，凭证包含私钥，请勿分享。退出程序后，可在同一台电脑、同一 Windows 用户下整体移动目录。更新程序时替换两个 EXE 和 `openvpn/`，保留 `data/`。

退出后删除解压目录即可移除程序和本地凭证。TAP 驱动及专用网卡会保留；需要移除网卡时，在设备管理器中确认其 `EZGameLAN-` 名称后删除。

## 服务端部署

服务端需要 Linux x64、systemd、Nginx 和 OpenVPN 2.6，OpenVPN 程序位于 `/usr/sbin/openvpn`。将 `packaging/server/` 和 `dist/ezlanctl-linux-amd64` 上传到服务器，保留这两个目录的相对位置。

将 `ezgamelan.r3t2.top` 解析到服务器公网 IP，并为该域名配置有效的 Nginx HTTPS 证书。防火墙和云安全组允许 `21111/UDP`、`443/TCP` 及 SSH 管理端口；加入服务的 `21112/TCP` 仅在服务器回环地址监听。

在包含 `packaging/` 和 `dist/` 的目录执行以下命令。将 `SSH_USER` 替换为管理端使用的现有 Linux 登录用户：

~~~bash
sudo bash packaging/server/install.sh \
  --host ezgamelan.r3t2.top \
  --enroll-url https://ezgamelan.r3t2.top/ezgamelan/v1/enroll \
  --admin-user SSH_USER \
  --binary ./dist/ezlanctl-linux-amd64
~~~

将 `packaging/server/nginx-ezgamelan-location.conf` 的内容加入该域名的 Nginx HTTPS `server` 块，然后检查并重载：

~~~bash
sudo nginx -t
sudo systemctl reload nginx
~~~

检查两个服务和维护定时任务，结果应均为 `active`：

~~~bash
systemctl is-active \
  ezgamelan-openvpn.service \
  ezgamelan-enroll.service \
  ezgamelan-maintain.timer
~~~

部署脚本会启用每日吊销维护。管理端提示吊销列表异常时，检查服务器文件权限和磁盘空间，再执行：

~~~bash
sudo ezlanctl maintain
~~~

需要修改公网域名时，更新配置并重启加入服务：

~~~bash
sudo ezlanctl config-update \
  --host NEW_HOST \
  --enroll-url https://NEW_HOST/ezgamelan/v1/enroll
sudo systemctl restart ezgamelan-enroll.service
~~~

修改只影响之后生成的邀请和凭证。已有邀请与凭证仍包含原地址，需保留原域名解析，或让玩家使用新邀请重新加入。

## 管理端使用

在管理员的 Windows 电脑上启用 OpenSSH Client，并配置 `%USERPROFILE%\.ssh\config`。以下示例使用 SSH 别名 `s0`，将 `SSH_USER` 替换为部署时指定的用户，并填写对应私钥路径：

~~~sshconfig
Host s0
    HostName ezgamelan.r3t2.top
    User SSH_USER
    IdentityFile ~/.ssh/id_ed25519
~~~

在终端执行 `ssh s0`，确认服务器指纹及密钥登录正常。管理端通过密钥登录，服务器部署时的 `--admin-user` 会授予该用户所需的管理命令权限。

双击 `EZGameLAN-Admin.exe`，输入 `s0` 并点击“连接”。连接成功后，浏览器会自动打开管理界面。

管理界面可查看设备和服务状态、在线设备的虚拟 IP 与流量，生成并复制一次性邀请，断开连接、吊销凭证、重启服务，以及查看和导出诊断信息。生成的邀请应单独发送给对应玩家。

关闭管理端时，点击页面中的“退出管理端”。服务器地址更新后，重新连接管理端以读取新配置。

也可登录服务器，在终端生成邀请或吊销设备。设备名使用 1–32 位字母、数字、短横线或下划线：

~~~bash
sudo ezlanctl invite DEVICE_NAME
sudo ezlanctl revoke DEVICE_NAME
~~~
