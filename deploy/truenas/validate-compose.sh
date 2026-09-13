#!/bin/sh
set -eu

compose=${1:-deploy/truenas/compose.yaml}
test -r "$compose"
python3 - "$compose" <<'PY'
import re, sys
text=open(sys.argv[1], encoding="utf-8").read()
required=("services:", "read_only: true", "cap_drop:", "no-new-privileges:true",
          "healthcheck:", "target: /library", "target: /certs")
missing=[x for x in required if x not in text]
if missing:
    raise SystemExit("missing required Compose properties: "+", ".join(missing))
if re.search(r'privileged\s*:\s*true|/var/run/docker\.sock|/dev/', text):
    raise SystemExit("prohibited privilege, Docker socket, or device access")
mounts=re.split(r"(?m)^      - type: bind\s*$", text)[1:]
libraries=[mount for mount in mounts if re.search(r"(?m)^        target: /library(?:/[^\s]+)?\s*$", mount)]
if not libraries or any(not re.search(r"(?m)^        read_only: true\s*$", mount) for mount in libraries):
    raise SystemExit("every library mount must be read-only")
if any(not re.search(r"(?m)^          create_host_path: false\s*$", mount) for mount in libraries):
    raise SystemExit("library mounts must not create missing host folders")
print("static compose policy: PASS")
PY
if command -v docker >/dev/null 2>&1; then
  docker compose --env-file deploy/truenas/.env.example -f "$compose" config >/dev/null
  echo "docker compose parser: PASS"
elif command -v podman-compose >/dev/null 2>&1; then
  podman-compose -f "$compose" config >/dev/null
  echo "podman-compose parser: PASS"
else
  echo "independent Compose parser unavailable" >&2
  exit 3
fi
