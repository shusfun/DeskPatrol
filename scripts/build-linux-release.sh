#!/usr/bin/env bash
set -euo pipefail

architecture="${1:-}"
version="${2:-}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
node_version="22.22.0"
export GOPROXY="https://goproxy.cn"
export GOSUMDB="sum.golang.google.cn"

if [[ "$architecture" != "amd64" && "$architecture" != "arm64" ]]; then
  echo "架构必须为 amd64 或 arm64" >&2
  exit 2
fi
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "版本必须使用 x.y.z" >&2
  exit 2
fi
if [[ ! "${MESHCENTRAL_SHA256:-}" =~ ^[a-f0-9]{64}$ ]]; then
  echo "必须显式设置 MESHCENTRAL_SHA256" >&2
  exit 2
fi

case "$architecture" in
  amd64) goarch="amd64"; node_arch="x64" ;;
  arm64) goarch="arm64"; node_arch="arm64" ;;
esac

work_dir="$(mktemp -d)"
trap 'rm -rf -- "$work_dir"' EXIT
stage="$work_dir/deskpatrol"
mkdir -p "$stage/bin" "$stage/web" "$stage/node" "$stage/meshcentral"

cd "$repo_root"
pnpm --filter @deskpatrol/admin build
pnpm --filter @deskpatrol/meshcentral-plugin deploy --prod --legacy "$stage/integrations/deskpatrol"
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X deskpatrol/internal/buildinfo.Version=$version" -o "$stage/bin/deskpatrol-server" ./cmd/server
cp -R frontend/apps/admin/dist "$stage/web/admin"
cp -R deploy "$stage/deploy"

node_archive="node-v${node_version}-linux-${node_arch}.tar.xz"
node_base="https://npmmirror.com/mirrors/node/v${node_version}"
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --output "$work_dir/SHASUMS256.txt" "$node_base/SHASUMS256.txt"
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --output "$work_dir/$node_archive" "$node_base/$node_archive"
expected_node_sha="$(awk -v filename="$node_archive" '$2 == filename { print $1 }' "$work_dir/SHASUMS256.txt")"
actual_node_sha="$(sha256sum "$work_dir/$node_archive" | awk '{print $1}')"
if [[ -z "$expected_node_sha" || "$expected_node_sha" != "$actual_node_sha" ]]; then
  echo "Node runtime SHA-256 校验失败" >&2
  exit 1
fi
tar -xJf "$work_dir/$node_archive" -C "$stage/node" --strip-components=1

npm_config_platform=linux npm_config_arch="$node_arch" npm --prefix "$stage/meshcentral" install --no-save --no-package-lock --omit=optional --ignore-scripts --registry=https://registry.npmmirror.com --replace-registry-host=always \
  ua-client-hints-js@0.1.2 image-size@2.0.2 pg@8.16.3 otplib@13.4.1
node scripts/fetch-meshcentral.mjs "$work_dir/meshcentral-1.2.5.tar.gz"
mkdir -p "$stage/meshcentral/node_modules/meshcentral"
tar -xzf "$work_dir/meshcentral-1.2.5.tar.gz" -C "$stage/meshcentral/node_modules/meshcentral" --strip-components=1
node scripts/patch-meshcentral.mjs "$stage/meshcentral/node_modules/meshcentral"
npm_config_platform=linux npm_config_arch="$node_arch" npm --prefix "$stage/meshcentral/node_modules/meshcentral" ci --omit=dev --ignore-scripts --registry=https://registry.npmmirror.com --replace-registry-host=always
node -e 'const { createRequire } = require("node:module"); const requireFromMeshCentral = createRequire(process.argv[1]); for (const moduleName of ["ua-client-hints-js", "image-size", "pg", "otplib"]) { requireFromMeshCentral.resolve(moduleName); }' \
  "$stage/meshcentral/node_modules/meshcentral/meshcentral.js"

mkdir -p "$repo_root/dist/linux/$architecture"
tar -czf "$repo_root/dist/linux/$architecture/deskpatrol-linux-$architecture-$version.tar.gz" -C "$work_dir" deskpatrol
echo "Linux $architecture Release 包已生成"
