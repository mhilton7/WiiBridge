#!/bin/bash
set -euo pipefail

version=${PROJECT_VERSION:-$(awk '$1 == "VERSION" { print $3; exit }' Makefile)}
prefix="dist/wiibridge-host-${version}"
digest_ref=$(cat "${prefix}.digest")
manifest_digest=${digest_ref##*@}
inputs="${prefix}.build-input.json"
commit=$(jq -er .revision "$inputs")
started_on=$(jq -er .started "$inputs")
source_worktree_dirty=$(jq -r '.dirty | select(type == "boolean")' "$inputs")
test "$source_worktree_dirty" = true || test "$source_worktree_dirty" = false
test "$manifest_digest" = "sha256:$(jq -er .digest "$inputs")"
test "$commit" = "$(git rev-parse HEAD)"
created=$(date -u +%Y-%m-%dT%H:%M:%SZ)
GOCACHE="${GOCACHE:-/tmp/wiibridge-go-cache}" \
GOPATH="${GOPATH:-/tmp/wiibridge-gopath}" \
  go list -m -json all |
  jq -s --arg version "$version" --arg created "$created" --arg digest "$manifest_digest" '
    map({SPDXID:("SPDXRef-Package-"+(.Path|gsub("[^A-Za-z0-9.-]";"-"))),
      name:.Path,versionInfo:(.Version // "local"),
      downloadLocation:(.Origin.URL // "NOASSERTION"),filesAnalyzed:false,
      licenseConcluded:"NOASSERTION",licenseDeclared:"NOASSERTION"}) |
    {spdxVersion:"SPDX-2.3",dataLicense:"CC0-1.0",
     SPDXID:"SPDXRef-DOCUMENT",name:("wiibridge-host-"+$version),
     documentNamespace:("https://wiibridge.invalid/spdx/host/"+$version+"/"+$digest),
     creationInfo:{created:$created,
       creators:["Tool: wiibridge-package-server"]},packages:.}' \
  > "${prefix}.sbom.spdx.json"
source_tree_sha=$(
  find Makefile go.mod go.sum versions.lock server shared config scripts deploy docs -type f -print0 |
    sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1
)
test "$source_tree_sha" = "$(jq -er .sourceTreeSha256 "$inputs")"
jq -n --arg digest "$manifest_digest" --arg commit "$commit" --arg started_on "$started_on" \
  --arg source_tree_sha "$source_tree_sha" \
  --arg finished_on "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --argjson source_worktree_dirty "$source_worktree_dirty" \
  '{predicateType:"https://slsa.dev/provenance/v1",
    subject:[{name:"wiibridge-host",digest:{sha256:($digest|sub("^sha256:";""))}}],
    buildDefinition:{buildType:"wiibridge/reproducible-oci-layout",
      externalParameters:{os:"linux",architecture:"amd64"},
      resolvedDependencies:[{uri:"git+local:wiibridge",
        digest:{gitCommit:$commit,sourceTreeSha256:$source_tree_sha}}]},
    runDetails:{builder:{id:"scripts/build-oci.sh"},
      metadata:{startedOn:$started_on,finishedOn:$finished_on,
        sourceWorktreeDirty:$source_worktree_dirty}}}' \
  > "${prefix}.provenance.json"
sha256sum "${prefix}.oci/index.json" > "${prefix}.oci.index.sha256"
