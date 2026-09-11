# Linux 一键安装

在使用 systemd 的 Linux 主机上执行一条命令：

```bash
curl -fsSL https://raw.githubusercontent.com/MiCat-S/mosdns-x/main/install.sh | sudo bash
```

脚本只执行这些操作：

1. 根据 `uname -m` 选择对应的 Linux Release。
2. 下载 zip 和 `SHA256SUMS`，精确校验所选文件。
3. 安装 `/usr/local/bin/mosdns`。
4. 如果 `/etc/mosdns/config.yaml` 不存在，安装 Release 自带的默认配置；已有配置保持原样。
5. 首次安装时调用 `mosdns service install` 创建并启用 systemd 服务，随后启动；再次运行时升级二进制并重启已有服务。

它不会安装 Caddy、软件包或防火墙规则，也不会创建服务账户、域名或管理员密码。运行前确保主机已有 `bash`、`curl`、`unzip`、`sha256sum` 和 systemd。

安装器会通过 Cloudflare trace 判断当前出口国家。检测到 `CN` 时，GitHub Release 下载默认使用 `https://gh-proxy.com/` 前缀；非中国出口或检测失败时直接连接 GitHub。检测过程不会输出公网 IP。

安装完成后检查：

```bash
mosdns version
systemctl status mosdns --no-pager
journalctl -u mosdns -n 50 --no-pager
```

默认安装 `v26.09.11`。指定其他日期版本：

```bash
curl -fsSL https://raw.githubusercontent.com/MiCat-S/mosdns-x/main/install.sh | \
  sudo bash -s -- --version vYY.MM.DD
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
