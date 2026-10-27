# DeskPatrol

DeskPatrol 是 Linux 管理端与 Windows Wails 客户端组成的设备监控桌面。设备连接、桌面采集和远程命令由固定版本 MeshCentral `1.2.5` / MeshAgent 提供。

## 本地开发

工具链固定为 Go `1.26.1`、Node `22.23.2`、pnpm `10.32.1` 和 Wails `3.0.0-alpha2.117`。

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

下载 Linux 部署包和 Windows 安装包前，会对 github.com 直连和几个 GitHub 加速镜像测速，选最快的线路。镜像只负责传输大文件。`SHA256SUMS` 和 `manifest.json` 始终直连 GitHub 获取，下载结果必须通过其中的 SHA-256 校验。线路列表在 `internal/releasemirror` 与 `scripts/install.sh` 中各有一份，由测试保证一致。

DeskPatrol、MeshCentral、MeshAgent 和 Wails 均不执行自动更新。升级可以通过 SSH 重新运行 `install.sh`，也可以由管理员在管理端“系统设置 → 系统更新”中手动触发。

自更新的特权部分由 root 运行的 `deskpatrol-update.service` 完成。后端只能把目标版本写入 `/var/lib/deskpatrol/update/request`，`deskpatrol-update.path` 检测到请求后启动更新器。更新器从 root 拥有的 `/etc/deskpatrol-updater.conf` 读取仓库地址，只接受高于当前版本的目标版本。它从 GitHub 获取目标版本的 `install.sh` 并校验 SHA-256 后执行。状态和日志写在 `/var/lib/deskpatrol-update/`。v0.1.22 及更早的服务器需要先通过 SSH 运行一次新版 `install.sh` 来安装自更新组件。

Windows 客户端支持在状态页重新激活或卸载 DeskPatrol。重新激活会移除本机设备绑定、MeshAgent 和客户端数据，然后使用新的连接密钥注册；卸载会通过系统卸载器清理 MeshAgent、安装目录和 `%ProgramData%\\DeskPatrol`。Windows 安装包使用简体中文安装与卸载向导。
