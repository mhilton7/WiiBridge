#!/bin/bash
set -euo pipefail

target=${1:?zero-w-armhf, pi4-arm64, or pi5-arm64}
case "$target" in
  zero-w-armhf)
    branch=master
    commit=314262cb286b8f33327a6f0cbabe14c625021ca0
    goarch=arm
    goarm=6
    ;;
  pi4-arm64|pi5-arm64)
    branch=arm64
    commit=ca8aeed0ae300c2a89f55ce9617d5f96a27e99e5
    goarch=arm64
    goarm=
    ;;
  *) echo "unsupported target: $target" >&2; exit 64 ;;
esac
source_root=$(pwd)
project_version=${PROJECT_VERSION:-$(awk '$1 == "VERSION" { print $3; exit }' Makefile)}
test -n "$project_version"
build_revision=$(git rev-parse HEAD)
build_timestamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)
build_dirty=$([ -z "$(git status --porcelain --untracked-files=normal)" ] && echo false || echo true)
source_tree_sha=$(
  find Makefile go.mod go.sum versions.lock server pi shared config scripts tests deploy docs -type f -print0 |
    sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1
)
tree="build/pi-gen-${target}"
binary="build/pi/${target}/wiibridge-pi-controller"
mkdir -p "$(dirname "$binary")"
GOCACHE="${GOCACHE:-/tmp/wiibridge-go-cache}" \
GOPATH="${GOPATH:-/tmp/wiibridge-gopath}" \
CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" GOARM="$goarm" \
  go build -trimpath \
  -ldflags="-s -w -buildid= -X main.productVersion=${project_version} -X main.gitCommit=${build_revision} -X main.buildTime=${build_timestamp} -X main.buildDirty=${build_dirty}" \
  -o "$binary" ./pi/controller
if test -e "$tree"; then
  # Never recurse through a leftover chroot mount during a rebuild. A failed
  # older builder may have left host device filesystems beneath this tree.
  python3 - "$source_root/$tree" <<'PY'
import json
from pathlib import Path
import subprocess
import sys

root = str(Path(sys.argv[1]).resolve())
mounts = json.loads(subprocess.check_output(
    ["findmnt", "--json", "--list", "--output", "TARGET"], text=True))
if any(item["target"] == root or item["target"].startswith(root + "/")
       for item in mounts["filesystems"]):
    raise SystemExit("Refusing to remove a pi-gen tree containing active mounts")
PY
  sudo rm -rf -- "$tree"
fi
git clone --filter=blob:none --branch "$branch" \
  https://github.com/RPi-Distro/pi-gen.git "$tree"
git -C "$tree" checkout --detach "$commit"
# Only the final board-customized stage is a release image.
touch "$tree/stage2/SKIP_IMAGES"
stage="$source_root/pi/packaging/pi-gen/common/stage-wiibridge"
sed "s|@WIIBRIDGE_STAGE@|$stage|" \
  "pi/packaging/pi-gen/${target}/config" > "$tree/config"
export WIIBRIDGE_BOARD_TARGET="$target"
export WIIBRIDGE_SOURCE="$source_root"
export PI_GEN_DIR="$source_root/$tree"
log="dist/wiibridge-${project_version}-${target}.build.log"
mkdir -p dist
jq -n --arg revision "$build_revision" --arg started "$build_timestamp" \
  --arg source_tree_sha "$source_tree_sha" --argjson dirty "$build_dirty" \
  '{revision:$revision,started:$started,sourceTreeSha256:$source_tree_sha,dirty:$dirty}' \
  > "dist/wiibridge-${project_version}-${target}.build-input.json"
if test -s "$log"; then
  report_dir="${FIRMWARE_REPORT_ROOT:-build/reports/firmware}/${target}/rejected-builds"
  mkdir -p "$report_dir"
  cp "$log" "$report_dir/$(date -u +%Y%m%dT%H%M%SZ).log"
fi
(
  cd "$tree"
  sudo --preserve-env=WIIBRIDGE_BOARD_TARGET,WIIBRIDGE_SOURCE,PI_GEN_DIR \
    unshare --mount --propagation private ./build.sh
) 2>&1 | tee "$source_root/$log"
image=$(find "$tree/deploy" -maxdepth 1 \
  -name "*wiibridge-${project_version}-${target}.img" -print -quit)
test -n "$image"
"$source_root/scripts/sanitize-firmware-image.sh" "$image"
cp --reflink=auto "$image" "dist/wiibridge-${project_version}-${target}.img"
