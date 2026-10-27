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

if [[ ! "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
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

# 候选下载线路，空字符串表示直连 github.com；必须与 internal/releasemirror 的 Prefixes 一致。
# 镜像只传输大文件，SHA256SUMS 始终直连 GitHub 获取并用于校验。
mirror_prefixes=(
  ""
  "https://ghfast.top/"
  "https://gh-proxy.com/"
  "https://gh-proxy.org/"
  "https://gh.llkk.cc/"
  "https://ghproxy.net/"
)
probe_bytes=1048576
curl_tls=(--fail --location --proto '=https' --tlsv1.2)

mirror_name() {
  if [[ -z "$1" ]]; then echo "github.com 直连"; else local name="${1#https://}"; echo "${name%/}"; fi
}

echo "从 GitHub 获取 v${version} 校验清单"
curl "${curl_tls[@]}" --silent --show-error --connect-timeout 15 --max-time 120 \
  --output "$temporary_dir/SHA256SUMS" "$release_base/SHA256SUMS"
expected_sha="$(awk -v filename="$archive" '$2 == filename { print $1 }' "$temporary_dir/SHA256SUMS")"
if [[ ! "$expected_sha" =~ ^[a-f0-9]{64}$ ]]; then
  echo "SHA256SUMS 中缺少 $archive" >&2
  exit 1
fi

echo "测速选择下载线路"
best_prefix=""
best_speed=0
found=0
for prefix in "${mirror_prefixes[@]}"; do
  result="$(curl "${curl_tls[@]}" --silent --range "0-$((probe_bytes - 1))" --output /dev/null \
    --connect-timeout 5 --max-time 10 --write-out '%{http_code} %{size_download} %{speed_download}' \
    "${prefix}${release_base}/${archive}" 2>/dev/null)" || true
  read -r code size speed <<<"${result:-000 0 0}"
  speed="${speed%%.*}"
  if [[ ("$code" == "206" || "$code" == "200") && "${size%%.*}" -ge "$probe_bytes" && "$speed" -gt 0 ]]; then
    printf '  %-20s %6d KB/s\n' "$(mirror_name "$prefix")" "$((speed / 1024))"
    if (( speed > best_speed )); then best_speed="$speed"; best_prefix="$prefix"; found=1; fi
  else
    printf '  %-20s 不可用（HTTP %s）\n' "$(mirror_name "$prefix")" "$code"
  fi
done
if (( found == 0 )); then
  echo "所有下载线路测速失败，请检查服务器网络" >&2
  exit 1
fi
echo "使用线路：$(mirror_name "$best_prefix")"

progress=(--silent --show-error)
if [[ -t 2 ]]; then progress=(--progress-bar); fi
curl "${curl_tls[@]}" "${progress[@]}" --connect-timeout 15 --speed-limit 10240 --speed-time 60 \
  --output "$temporary_dir/$archive" "${best_prefix}${release_base}/${archive}"

actual_sha="$(sha256sum "$temporary_dir/$archive" | awk '{print $1}')"
if [[ "$expected_sha" != "$actual_sha" ]]; then
  echo "Linux 部署包 SHA-256 校验失败（线路：$(mirror_name "$best_prefix")）" >&2
  exit 1
fi
echo "部署包校验通过"

release_dir="$install_root/releases/$version"
install -d -m 0755 "$release_dir" /var/lib/deskpatrol /var/log/deskpatrol
install -d -m 0750 /etc/deskpatrol
tar -xzf "$temporary_dir/$archive" -C "$release_dir" --strip-components=1 --no-same-owner
chown -R root:root "$release_dir"

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

# 自更新组件：后端（deskpatrol 用户）只能写 /var/lib/deskpatrol/update/request，
# root 更新器的脚本、仓库配置和状态目录都由 root 拥有，后端只读。
install -d -o deskpatrol -g deskpatrol -m 0750 /var/lib/deskpatrol/update
install -d -o root -g root -m 0755 /var/lib/deskpatrol-update /usr/local/sbin
# 先写临时文件再 rename，避免覆盖正在执行的更新器脚本。
install -o root -g root -m 0755 "$release_dir/deploy/systemd/deskpatrol-update.sh" /usr/local/sbin/.deskpatrol-update.new
mv -f /usr/local/sbin/.deskpatrol-update.new /usr/local/sbin/deskpatrol-update
printf 'REPOSITORY=%s\nINSTALL_ROOT=%s\n' "$repository" "$install_root" > /etc/.deskpatrol-updater.conf.new
chmod 0644 /etc/.deskpatrol-updater.conf.new
mv -f /etc/.deskpatrol-updater.conf.new /etc/deskpatrol-updater.conf
install -m 0644 "$release_dir/deploy/systemd/deskpatrol-update.service" /etc/systemd/system/deskpatrol-update.service
install -m 0644 "$release_dir/deploy/systemd/deskpatrol-update.path" /etc/systemd/system/deskpatrol-update.path

systemctl daemon-reload
systemctl enable deskpatrol.service deskpatrol-meshcentral.path deskpatrol-update.path
systemctl start deskpatrol-update.path

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
