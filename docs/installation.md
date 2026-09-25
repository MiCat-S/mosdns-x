# Linux 一键安装

在使用 systemd 的 Linux 主机上执行一条命令：

```bash
curl -fsSL https://raw.githubusercontent.com/MiCat-S/mosdns-x/main/install.sh | sudo bash
```

脚本只执行这些操作：

1. 根据 `uname -m` 选择对应的 Linux Release。
2. 下载 zip 和 `SHA256SUMS`，精确校验所选文件。`SHA256SUMS` 始终直连 GitHub 获取，不经过下载代理。
3. 安装 `/usr/local/bin/mosdns`。
4. 如果 `/etc/mosdns/config.yaml` 不存在，安装 Release 自带的默认配置；已有配置保持原样。
5. 首次安装时调用 `mosdns service install` 创建 systemd 服务；在启用或启动服务前检查控制存储。控制模式未启用或管理员已存在时正常启动；未初始化时会在交互终端询问管理员用户名和两次密码，并通过标准输入初始化。非交互运行且未初始化时，脚本不启动服务，并输出可直接执行的初始化命令。
6. 写入 `/etc/systemd/system/mosdns.service.d/10-mosdns-x.conf`：让 mosdns 在网络和本机 MySQL（`mysqld`/`mysql`/`mariadb`，不存在的会被忽略）之后启动，并把重启间隔设为 5 秒。`mosdns service install` 生成的 unit 两者都没有，开机时 mosdns 可能先于 MySQL 启动、打不开控制库，然后按固定的 120 秒间隔才重试。
7. 再次运行时升级二进制并重启已有服务。如果旧服务仍引用 `/etc/mosdns/mosdns` 等过期路径，脚本会重新安装服务，使 `ExecStart` 和 `ConditionFileIsExecutable` 指向 `/usr/local/bin/mosdns`。

它不会安装 Caddy、软件包或防火墙规则，也不会创建服务账户、域名或管理员密码。运行前确保主机已有 `bash`、`curl`、`unzip`、`sha256sum` 和 systemd。

安装器会通过 Cloudflare trace 判断当前出口国家。检测到 `CN` 时，GitHub Release 下载默认使用 `https://gh-proxy.com/` 前缀；非中国出口或检测失败时直接连接 GitHub。检测过程不会输出公网 IP。

使用代理时，`SHA256SUMS` 仍然直连 GitHub 获取。如果两者都走同一个代理，校验就退化成只能发现传输损坏：能够投递被篡改安装包的那一跳，同样可以投递与之匹配的校验和。直连失败时脚本会退回代理并打印明确告警，此时校验和与安装包同源，无法独立证明发布件未被篡改；在意这一点的话，请用 `--no-github-proxy` 强制直连，或按 [构建说明](building.md) 自行构建。

Release 目前由本地构建后手工上传，没有签名或 provenance 证明。对供应链有更高要求的部署应自行构建二进制。

安装完成后检查：

```bash
mosdns version
systemctl status mosdns --no-pager
journalctl -u mosdns -n 50 --no-pager
```

如配置启用了 `control`，也可检查管理员初始化状态：

```bash
mosdns control status --config /etc/mosdns/config.yaml
```

该命令只输出 `ready`、`disabled`、`uninitialized` 或 `storage_error`，不会输出数据库 DSN 或密码。对应退出码依次为 0、10、11、12；详情见[运维文档](operations.md#控制存储状态)。脚本升级已初始化的实例不会再次询问管理员密码；检查 bbolt 存储前会先停止正在运行的服务，然后在状态正常时重新启动。

默认安装 `v26.09.25`。指定其他日期版本或同日修订版：

```bash
curl -fsSL https://raw.githubusercontent.com/MiCat-S/mosdns-x/main/install.sh | \
  sudo bash -s -- --version vYY.MM.DD.N
```

需要绕过自动判断时，可以明确禁用代理或指定 HTTPS 代理前缀：

```bash
curl -fsSL https://raw.githubusercontent.com/MiCat-S/mosdns-x/main/install.sh | \
  sudo bash -s -- --no-github-proxy

curl -fsSL https://raw.githubusercontent.com/MiCat-S/mosdns-x/main/install.sh | \
  sudo bash -s -- --github-proxy https://gh-proxy.com/
```

默认配置监听 `127.0.0.1:5533` 的 UDP 和 TCP。修改 `/etc/mosdns/config.yaml` 后执行：

```bash
sudo systemctl restart mosdns
```

查看脚本参数和当前机器对应的资产名：

```bash
bash install.sh --help
bash install.sh --print-asset
```
