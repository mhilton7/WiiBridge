#!/bin/bash
set -euo pipefail

target=${1:?target}
version=${PROJECT_VERSION:-$(awk '$1 == "VERSION" { print $3; exit }' Makefile)}
prefix="dist/wiibridge-${version}-${target}"
image="${prefix}.img"
test -s "$image"
inputs="${prefix}.build-input.json"
app_commit=$(jq -er .revision "$inputs")
started_on=$(jq -er .started "$inputs")
source_dirty=$(jq -r '.dirty | select(type == "boolean")' "$inputs")
test "$source_dirty" = true || test "$source_dirty" = false
source_tree_sha=$(
  find Makefile go.mod go.sum versions.lock server pi shared config scripts tests deploy docs -type f -print0 |
    sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1
)
test "$app_commit" = "$(git rev-parse HEAD)"
test "$source_tree_sha" = "$(jq -er .sourceTreeSha256 "$inputs")"
"tests/firmware/offline/validate.sh" "$target"
sha256sum "$image" > "${image}.sha256"
bmaptool create -o "${prefix}.bmap" "$image"
xz -T0 -9 --keep --force "$image"
sha256sum "${image}.xz" > "${image}.xz.sha256"
original=$(cut -d' ' -f1 < "${image}.sha256")
expanded=$(xz -dc "${image}.xz" | sha256sum | cut -d' ' -f1)
test "$original" = "$expanded"
packages="${prefix}.packages.txt"
sbom="${prefix}.sbom.spdx.json"
created=$(date -u +%Y-%m-%dT%H:%M:%SZ)
jq -Rn --arg target "$target" --arg version "$version" \
  --arg created "$created" --arg revision "$app_commit" \
  '[inputs | select(length>0) | split("=") | {
    SPDXID:("SPDXRef-Package-"+(.[0]|gsub("[^A-Za-z0-9.-]";"-"))),
    name:.[0],versionInfo:(.[1:]|join("=")),downloadLocation:"NOASSERTION",
    filesAnalyzed:false,licenseConcluded:"NOASSERTION",licenseDeclared:"NOASSERTION"}] |
  {spdxVersion:"SPDX-2.3",dataLicense:"CC0-1.0",
   SPDXID:"SPDXRef-DOCUMENT",name:("wiibridge-"+$target),
   documentNamespace:("https://wiibridge.invalid/spdx/"+$version+"/"+$target+"/"+$revision),
   creationInfo:{created:$created,
     creators:["Tool: wiibridge-package-firmware"]},packages:.}' \
  < "$packages" > "$sbom"
image_sha=$(cut -d' ' -f1 < "${image}.sha256")
xz_sha=$(cut -d' ' -f1 < "${image}.xz.sha256")
jq -n --arg target "$target" --arg image_sha "$image_sha" --arg xz_sha "$xz_sha" \
  --arg app_commit "$app_commit" --arg started_on "$started_on" \
  --argjson source_dirty "$source_dirty" \
  --arg source_tree_sha "$source_tree_sha" \
  --arg finished_on "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg pi_gen_armhf "314262cb286b8f33327a6f0cbabe14c625021ca0" \
  --arg pi_gen_arm64 "ca8aeed0ae300c2a89f55ce9617d5f96a27e99e5" \
  '{predicateType:"https://slsa.dev/provenance/v1",
    subject:[{name:($target+".img"),digest:{sha256:$image_sha}},
             {name:($target+".img.xz"),digest:{sha256:$xz_sha}}],
    buildDefinition:{buildType:"wiibridge/pi-gen",
      externalParameters:{target:$target},
      resolvedDependencies:[
       {uri:"git+https://github.com/RPi-Distro/pi-gen@master",digest:{gitCommit:$pi_gen_armhf}},
       {uri:"git+https://github.com/RPi-Distro/pi-gen@arm64",digest:{gitCommit:$pi_gen_arm64}},
       {uri:"git+local:wiibridge",
        digest:{gitCommit:$app_commit,sourceTreeSha256:$source_tree_sha}}]},
    runDetails:{builder:{id:"wiibridge-build-firmware.sh"},
      metadata:{invocationId:($target+"-"+$app_commit+"-"+$started_on),
        startedOn:$started_on,finishedOn:$finished_on,
        sourceWorktreeDirty:$source_dirty}}}' \
  > "${prefix}.provenance.json"
report_dir="${FIRMWARE_REPORT_ROOT:-build/reports/firmware}/${target}"
mkdir -p "$report_dir"
cp "${prefix}.offline-validation.json" "${prefix}.packages.txt" \
  "${prefix}.sbom.spdx.json" "${prefix}.provenance.json" \
  "${prefix}.build.log" "$report_dir/"
