#!/bin/sh
set -eu

version=${PROJECT_VERSION:-0.1.0-rc.1}
out=${1:-dist/wiibridge-host-${version}.oci}
root=build/container-root
cache=${GOCACHE:-/tmp/wiibridge-go-cache}
gopath=${GOPATH:-/tmp/wiibridge-gopath}
commit=$(git rev-parse HEAD)
created=$(date -u +%Y-%m-%dT%H:%M:%SZ)
source=${OCI_SOURCE:-https://github.com/OWNER/WiiBridge}
if [ -z "$(git status --porcelain --untracked-files=normal)" ]; then
  dirty=false
else
  dirty=true
fi
source_tree_sha=$(
  find Makefile go.mod go.sum versions.lock server shared config scripts deploy docs -type f -print0 |
    sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1
)
mkdir -p "$root" "$out/blobs/sha256"
GOCACHE="$cache" GOPATH="$gopath" CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath \
  -ldflags="-s -w -buildid= -X main.gitCommit=$commit -X main.buildTime=$created -X main.buildDirty=$dirty" \
  -o "$root/wiibridge-host" ./server/host-daemon
chmod 0555 "$root/wiibridge-host"
layer_tmp=$(mktemp)
config_tmp=$(mktemp)
manifest_tmp=$(mktemp)
trap 'rm -f "$layer_tmp" "$config_tmp" "$manifest_tmp"' EXIT
tar --sort=name --mtime='UTC 2026-07-24' --owner=568 --group=568 \
  --numeric-owner -C "$root" -cf "$layer_tmp" wiibridge-host
layer_digest=$(sha256sum "$layer_tmp" | cut -d' ' -f1)
layer_size=$(wc -c < "$layer_tmp")
mv "$layer_tmp" "$out/blobs/sha256/$layer_digest"
printf '{"architecture":"amd64","os":"linux","created":"%s","config":{"User":"568:568","Entrypoint":["/wiibridge-host"],"ExposedPorts":{"8445/tcp":{},"10809/tcp":{}},"Env":["PATH=/"],"Labels":{"org.opencontainers.image.revision":"%s","org.opencontainers.image.version":"%s","org.opencontainers.image.created":"%s","org.opencontainers.image.source":"%s"}},"rootfs":{"type":"layers","diff_ids":["sha256:%s"]},"history":[{"created":"%s","created_by":"wiibridge OCI builder"}]}' "$created" "$commit" "$version" "$created" "$source" "$layer_digest" "$created" > "$config_tmp"
config_digest=$(sha256sum "$config_tmp" | cut -d' ' -f1)
config_size=$(wc -c < "$config_tmp")
mv "$config_tmp" "$out/blobs/sha256/$config_digest"
printf '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:%s","size":%s},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"sha256:%s","size":%s}],"annotations":{"org.opencontainers.image.title":"wiibridge-host","org.opencontainers.image.version":"%s","org.opencontainers.image.revision":"%s","org.opencontainers.image.created":"%s","org.opencontainers.image.source":"%s"}}' "$config_digest" "$config_size" "$layer_digest" "$layer_size" "$version" "$commit" "$created" "$source" > "$manifest_tmp"
manifest_digest=$(sha256sum "$manifest_tmp" | cut -d' ' -f1)
manifest_size=$(wc -c < "$manifest_tmp")
mv "$manifest_tmp" "$out/blobs/sha256/$manifest_digest"
printf '{"imageLayoutVersion":"1.0.0"}\n' > "$out/oci-layout"
printf '{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:%s","size":%s,"annotations":{"org.opencontainers.image.ref.name":"wiibridge-host:%s"}}]}\n' "$manifest_digest" "$manifest_size" "$version" > "$out/index.json"
printf 'wiibridge-host:%s@sha256:%s\n' "$version" "$manifest_digest" | tee "dist/wiibridge-host-${version}.digest"
jq -n --arg revision "$commit" --arg started "$created" \
  --arg digest "$manifest_digest" --arg source_tree_sha "$source_tree_sha" \
  --argjson dirty "$dirty" \
  '{revision:$revision,started:$started,digest:$digest,
    sourceTreeSha256:$source_tree_sha,dirty:$dirty}' \
  > "dist/wiibridge-host-${version}.build-input.json"
