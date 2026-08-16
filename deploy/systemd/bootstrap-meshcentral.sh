#!/usr/bin/env bash
set -euo pipefail

node_path="/opt/deskpatrol/current/node/bin/node"
meshcentral_path="/opt/deskpatrol/current/meshcentral/node_modules/meshcentral/meshcentral.js"
config_path="/etc/deskpatrol/meshcentral-config.json"
administrator_id="user//admin"

users="$($node_path "$meshcentral_path" --configfile "$config_path" --listuserids)"
if ! grep -Fxq "$administrator_id" <<<"$users"; then
  password="$($node_path -e 'process.stdout.write(require("node:crypto").randomBytes(32).toString("hex"))')"
  "$node_path" "$meshcentral_path" --configfile "$config_path" --createaccount admin --pass "$password"
  unset password
fi

"$node_path" "$meshcentral_path" --configfile "$config_path" --adminaccount admin
