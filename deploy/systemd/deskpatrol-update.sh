#!/usr/bin/env bash
# DeskPatrol root 更新器：由 deskpatrol-update.path 在后端写入更新请求后触发。
# 只信任 root 拥有的 /etc/deskpatrol-updater.conf；请求文件只提供目标版本号，且必须高于当前版本。
set -euo pipefail

request_path="/var/lib/deskpatrol/update/request"
state_dir="/var/lib/deskpatrol-update"
config_path="/etc/deskpatrol-updater.conf"
log_path="$state_dir/update.log"
version=""
current=""
reported=0

write_status() {
  local temporary="$state_dir/.status.new"
  printf 'STATE=%s\nVERSION=%s\nFROM=%s\nMESSAGE=%s\nUPDATED_AT=%s\n' "$1" "$version" "$current" "$2" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$temporary"
  chmod 0644 "$temporary"
  mv -f "$temporary" "$state_dir/status"
}

fail() {
  echo "更新失败：$1" >&2
  write_status failed "$1"
  reported=1
  exit 1
}

on_exit() {
  local code=$?
  rm -rf -- "${work_dir:-}"
  if (( code != 0 && reported == 0 )); then write_status failed "更新器异常退出（exit=$code），详见更新日志"; fi
}

install -d -o root -g root -m 0755 "$state_dir"
work_dir="$(mktemp -d)"
trap on_exit EXIT
trap 'exit 143' TERM INT

# 先把请求移到 root 私有目录再读取，避免读取过程中被替换；只接受普通文件。
if [[ ! -e "$request_path" && ! -L "$request_path" ]]; then
  echo "没有待处理的更新请求"
  exit 0
fi
mv -f -- "$request_path" "$work_dir/request"
if [[ -L "$work_dir/request" || ! -f "$work_dir/request" ]]; then fail "更新请求不是普通文件"; fi
if (( $(stat -c %s "$work_dir/request") > 64 )); then fail "更新请求内容过长"; fi
if [[ ! "$(cat "$work_dir/request")" =~ ^VERSION=([0-9]+\.[0-9]+\.[0-9]+)$ ]]; then fail "更新请求格式不正确"; fi
version="${BASH_REMATCH[1]}"

if [[ ! -f "$config_path" ]]; then fail "缺少更新器配置 $config_path"; fi
repository="$(sed -n 's/^REPOSITORY=//p' "$config_path" | head -n 1)"
install_root="$(sed -n 's/^INSTALL_ROOT=//p' "$config_path" | head -n 1)"
if [[ ! "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then fail "更新器配置中的仓库地址不正确"; fi
if [[ ! "$install_root" =~ ^/[A-Za-z0-9_./-]+$ ]]; then fail "更新器配置中的安装目录不正确"; fi

current_dir="$(readlink -f "$install_root/current")" || fail "无法解析当前安装版本"
current="$(basename "$current_dir")"
if [[ ! "$current" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then fail "当前安装版本 $current 格式不正确"; fi
if [[ "$version" == "$current" || "$(printf '%s\n%s\n' "$current" "$version" | sort -V | tail -n 1)" != "$version" ]]; then
  fail "目标版本 $version 必须高于当前版本 $current"
fi

: > "$log_path"
chmod 0644 "$log_path"
exec >>"$log_path" 2>&1
echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] 开始从 $current 更新到 $version"
write_status running "正在下载并安装 v$version"

release_base="https://github.com/${repository}/releases/download/v${version}"
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --connect-timeout 15 --max-time 120 \
  --output "$work_dir/SHA256SUMS" "$release_base/SHA256SUMS" || fail "从 GitHub 获取 SHA256SUMS 失败"
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --connect-timeout 15 --max-time 120 \
  --output "$work_dir/install.sh" "$release_base/install.sh" || fail "从 GitHub 获取 install.sh 失败"
expected_sha="$(awk '$2 == "install.sh" { print $1 }' "$work_dir/SHA256SUMS")"
actual_sha="$(sha256sum "$work_dir/install.sh" | awk '{print $1}')"
if [[ ! "$expected_sha" =~ ^[a-f0-9]{64}$ || "$expected_sha" != "$actual_sha" ]]; then fail "install.sh SHA-256 校验失败"; fi

bash "$work_dir/install.sh" --repository "$repository" --version "$version" --install-root "$install_root" || fail "执行 v$version install.sh 失败，详见更新日志"
echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] 已更新到 $version"
write_status succeeded "已更新到 v$version"
