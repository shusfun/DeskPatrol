#!/usr/bin/env bash
set -euo pipefail

repository=""
version=""
install_root="/opt/deskpatrol"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repository) repository="${2:-}"; shift 2 ;;
    --version) version="${2:-}"; shift 2 ;;
    --install-root) install_root="${2:-}"; shift 2 ;;
    *) echo "未知参数：$1" >&2; exit 2 ;;
  esac
done

if [[ ! "$repository" =~ ^[^/]+/[^/]+$ ]]; then
  echo "必须通过 --repository 指定 owner/repository" >&2
  exit 2
fi
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "必须通过 --version 指定 x.y.z" >&2
  exit 2
fi
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "DeskPatrol 部署脚本仅支持 Linux" >&2
  exit 1
fi
if [[ "${EUID}" -ne 0 ]]; then
  echo "安装 systemd 服务需要 root 权限" >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64) architecture="amd64" ;;
  aarch64|arm64) architecture="arm64" ;;
  *) echo "不支持的 Linux 架构：$(uname -m)" >&2; exit 1 ;;
esac

release_base="https://github.com/${repository}/releases/download/v${version}"
archive="deskpatrol-linux-${architecture}-${version}.tar.gz"
temporary_dir="$(mktemp -d)"
trap 'rm -rf -- "$temporary_dir"' EXIT

curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
  --output "$temporary_dir/SHA256SUMS" "$release_base/SHA256SUMS"
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
  --output "$temporary_dir/$archive" "$release_base/$archive"

expected_sha="$(awk -v filename="$archive" '$2 == filename { print $1 }' "$temporary_dir/SHA256SUMS")"
actual_sha="$(sha256sum "$temporary_dir/$archive" | awk '{print $1}')"
if [[ -z "$expected_sha" || "$expected_sha" != "$actual_sha" ]]; then
  echo "Linux 部署包 SHA-256 校验失败" >&2
  exit 1
fi

release_dir="$install_root/releases/$version"
install -d -m 0755 "$release_dir" /var/lib/deskpatrol /var/log/deskpatrol
install -d -m 0750 /etc/deskpatrol
tar -xzf "$temporary_dir/$archive" -C "$release_dir" --strip-components=1

if ! id deskpatrol >/dev/null 2>&1; then
  useradd --system --home /var/lib/deskpatrol --shell /usr/sbin/nologin deskpatrol
fi
chown -R deskpatrol:deskpatrol /etc/deskpatrol /var/lib/deskpatrol /var/log/deskpatrol
install -d -o deskpatrol -g deskpatrol -m 0700 \
  /var/lib/deskpatrol/meshcentral \
  /var/lib/deskpatrol/meshcentral-files \
  /var/lib/deskpatrol/meshcentral/plugins/deskpatrol
cp -R "$release_dir/integrations/deskpatrol/." /var/lib/deskpatrol/meshcentral/plugins/deskpatrol/
chown -R deskpatrol:deskpatrol /var/lib/deskpatrol/meshcentral/plugins/deskpatrol
install -m 0644 "$release_dir/deploy/systemd/deskpatrol.service" /etc/systemd/system/deskpatrol.service
install -m 0644 "$release_dir/deploy/systemd/deskpatrol-meshcentral.service" /etc/systemd/system/deskpatrol-meshcentral.service
install -m 0644 "$release_dir/deploy/systemd/deskpatrol-meshcentral.path" /etc/systemd/system/deskpatrol-meshcentral.path
install -m 0644 "$release_dir/deploy/nginx/deskpatrol.conf.example" /etc/deskpatrol/nginx.conf.example
systemctl daemon-reload
systemctl enable deskpatrol.service deskpatrol-meshcentral.path

if [[ -f /etc/deskpatrol/config.json ]]; then
  runuser -u deskpatrol -- "$release_dir/bin/deskpatrol-server" migrate --config /etc/deskpatrol/config.json
  ln -sfn "$release_dir" "$install_root/current"
  systemctl restart deskpatrol.service deskpatrol-meshcentral.service
  systemctl start deskpatrol-meshcentral.path
else
  ln -sfn "$release_dir" "$install_root/current"
  systemctl start deskpatrol.service deskpatrol-meshcentral.path
fi

echo "DeskPatrol $version 已安装。请通过 Nginx 暴露服务后访问 Setup 页面。"
