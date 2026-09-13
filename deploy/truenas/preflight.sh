#!/bin/sh
set -eu

report=${1:-preflight-report.json}
version=UNKNOWN
backend=UNSUPPORTED_OR_UNKNOWN
arch=$(uname -m)
if command -v midclt >/dev/null 2>&1; then
  version=$(midclt call system.version | tr -d '"')
  if midclt call app.query >/dev/null 2>&1; then
    backend=COMPOSE_CUSTOM_APPS_CANDIDATE
  fi
elif test -r /etc/version; then
  version=$(sed -n '1p' /etc/version)
fi
case "$arch" in
  x86_64) image_arch=amd64 ;;
  aarch64) image_arch=arm64 ;;
  *) image_arch=UNSUPPORTED ;;
esac
wii_library_path=${WIIBRIDGE_WII_LIBRARY_PATH:-${WIIBRIDGE_LIBRARY_PATH:-}}
gamecube_library_path=${WIIBRIDGE_GAMECUBE_LIBRARY_PATH:-${WIIBRIDGE_LIBRARY_PATH:-}}
if test -z "$wii_library_path" || test -z "$gamecube_library_path"; then
  echo "Set a shared library path or both platform library paths" >&2
  exit 2
fi
for variable in WIIBRIDGE_CONFIG_PATH \
  WIIBRIDGE_DATA_PATH WIIBRIDGE_CERTS_PATH WIIBRIDGE_LOGS_PATH \
  WIIBRIDGE_BACKUPS_PATH WIIBRIDGE_HTTPS_BIND WIIBRIDGE_NBD_BIND; do
  value=$(printenv "$variable" || true)
  test -n "$value" || {
    echo "$variable is required" >&2
    exit 2
  }
done
for directory in "$wii_library_path" "$gamecube_library_path" "$WIIBRIDGE_CONFIG_PATH" \
  "$WIIBRIDGE_DATA_PATH" "$WIIBRIDGE_CERTS_PATH" \
  "$WIIBRIDGE_LOGS_PATH" "$WIIBRIDGE_BACKUPS_PATH"; do
  test -d "$directory" || {
    echo "missing directory: $directory" >&2
    exit 2
  }
done
library_mode=$(findmnt -no OPTIONS -T "$wii_library_path" 2>/dev/null || echo UNKNOWN)
gamecube_library_mode=$(findmnt -no OPTIONS -T "$gamecube_library_path" 2>/dev/null || echo UNKNOWN)
jq -n \
  --arg version "$version" --arg backend "$backend" --arg arch "$arch" \
  --arg image_arch "$image_arch" --arg library_mode "$library_mode" \
  --arg gamecube_library_mode "$gamecube_library_mode" \
  '{truenas_version:$version,apps_backend:$backend,host_architecture:$arch,
    required_image_architecture:$image_arch,library_mount_options:$library_mode,
    wii_library_mount_options:$library_mode,gamecube_library_mount_options:$gamecube_library_mode,
    compatible:($backend=="COMPOSE_CUSTOM_APPS_CANDIDATE" and $image_arch!="UNSUPPORTED")}' \
  > "$report"
cat "$report"
test "$backend" = COMPOSE_CUSTOM_APPS_CANDIDATE
