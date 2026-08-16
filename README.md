# DeskPatrol

DeskPatrol 是 Linux 管理端与 Windows Wails 客户端组成的设备监控桌面。设备连接、桌面采集和远程命令由固定版本 MeshCentral `1.2.5` / MeshAgent 提供。

## 本地开发

工具链固定为 Go `1.26.1`、Node `22.22.0`、pnpm `10.32.1` 和 Wails `3.0.0-alpha2.117`。

```bash
pnpm install --frozen-lockfile --child-concurrency=2
pnpm run repo -- build admin
pnpm run repo -- build backend
pnpm run repo -- dev start
```

未初始化时服务端不依赖 PostgreSQL 即可显示 Setup。默认地址是 `http://127.0.0.1:18123`。

## GitHub Release

在 GitHub Actions 手动运行 `Release`，输入不带 `v` 的版本号。仓库必须预先配置 `MESHCENTRAL_SHA256` Secret，其值是 MeshCentral `1.2.5` tag 完整源码包的 64 位小写 SHA-256；缺失或不匹配时构建直接失败。

Release 包含：

- Linux amd64、arm64 完整包
- Windows x64、ARM64 安装包
- `install.sh`
- `manifest.json`、`SHA256SUMS`、第三方许可证清单

## Linux 安装

```bash
curl --fail --location --output install.sh \
  "https://github.com/OWNER/REPOSITORY/releases/download/vVERSION/install.sh"
sudo bash install.sh --repository OWNER/REPOSITORY --version VERSION
```

安装目录为 `/opt/deskpatrol`，配置目录为 `/etc/deskpatrol`，持久数据目录为 `/var/lib/deskpatrol`。安装器启动 Setup 服务并安装 MeshCentral 配置监视单元；Setup 写入最后一个配置文件后才启动 MeshCentral。

Nginx 示例会安装到 `/etc/deskpatrol/nginx.conf.example`。配置证书与域名后，管理端使用 HTTPS `443`，Agent-only WSS 使用 `8443`。

Setup 完成后，Linux 服务在后台下载当前 Release 的 Windows 安装包并校验 manifest 与 SHA-256。客户端和浏览器只从 DeskPatrol Linux 服务下载，不获取 GitHub 下载地址。

DeskPatrol、MeshCentral、MeshAgent 和 Wails 均不执行自动更新。升级必须重新构建 Release 并由管理员显式安装。
