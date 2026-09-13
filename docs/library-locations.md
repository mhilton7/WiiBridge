# Separate Wii and GameCube library locations

The Host supports independent read-only source directories:

| Host environment setting | Purpose | Default |
| --- | --- | --- |
| `WIIBRIDGE_WII_LIBRARY` | Wii WBFS source directory inside the container | `WIIBRIDGE_LIBRARY`, or `/library` |
| `WIIBRIDGE_GAMECUBE_LIBRARY` | GameCube source directory inside the container | `WIIBRIDGE_LIBRARY`, or `/library` |

Existing shared-root installations keep working without configuration changes.
These settings require a Host build containing the separate-library-paths change;
the previously published August 28 image does not implement them. Updated Hosts
advertise `separate-library-paths-v1` in the compatibility panel.

## Two folders inside the existing library mount

If the existing read-only `/library` mount contains `Wii` and `GameCube`
subdirectories, set the Host environment to:

```yaml
WIIBRIDGE_WII_LIBRARY: /library/Wii
WIIBRIDGE_GAMECUBE_LIBRARY: /library/GameCube
```

Use the actual spelling and capitalization of your directories. The Host reads
these environment settings; changing the example configuration YAML alone does
not change its runtime paths.

## Two separate TrueNAS datasets

Use the standalone
[`compose.separate-libraries.yaml`](../deploy/truenas/compose.separate-libraries.yaml)
definition and set these values in the Compose environment:

```dotenv
WIIBRIDGE_WII_LIBRARY_PATH=/mnt/POOL/WII_DATASET
WIIBRIDGE_GAMECUBE_LIBRARY_PATH=/mnt/POOL/GAMECUBE_DATASET
```

The definition mounts them read-only at `/library/wii` and `/library/gamecube`
and sets the corresponding Host environment variables. It refuses to create
missing host directories. If a dedicated host path is omitted, the definition
falls back to `WIIBRIDGE_LIBRARY_PATH` for that platform.

For TrueNAS **Install via YAML**, use the same two bind mounts and environment
settings in your resolved definition. Keep the existing `/data`, `/config`,
certificate, and save-storage datasets. Pin the updated Host image by its
published immutable digest before installing it.

## Recover after moving a library

1. Stop gameplay and detach the Pi's USB export before changing library mounts.
2. Update the container's bind mounts and platform environment settings, then
   restart the Host with the updated image.
3. Check the Wii and GameCube source cards in the dashboard. Rescan each library.
4. If a moved dataset reports `SOURCE-MOUNT-MISSING` or
   `SOURCE-IDENTITY-CHANGED`, open **Moved this library?** on its source card,
   confirm the intended location, and select **Accept location and rescan**.
5. For GameCube, select **Build GameCube Library** or its update control after
   the scan, then activate the validated generation and reconnect the Pi.

Ordinary scans continue to reject unexpected mount changes. Explicit recovery
checks that the replacement is a readable, read-only directory, completes
discovery, and rechecks the mount before publishing its identity and catalog
in one SQLite transaction. A previously populated library cannot be replaced
by a scan with no valid games. Missing or partially scanned sources preserve
the existing catalog, generations, and save data.

Wii and GameCube scans are independent when their configured roots differ.
When both use the exact same root, they are scanned and committed together.
An outdated GameCube generation remains unavailable while a replacement is
built from the newly scanned paths; it cannot make those fresh sources
unavailable merely because its old paths no longer exist.

## API

- `GET /api/v1/sources` includes `sources.wii` and `sources.gamecube`.
  The legacy `source` field continues to describe the Wii source.
- `POST /api/v1/scan` accepts form field `platform=wii`, `gamecube`, or `all`
  (the default). Results report each requested library separately. With
  separate roots, a failed library does not roll back a successful other scan.
- `POST /api/v1/sources/relocate` requires `platform=wii` or `gamecube` and
  `confirm=relocate`, with the existing administrator authentication and CSRF
  protection. It accepts only the configured location, not an arbitrary path.
- Diagnostic exports include both platform source records.

The standalone separate-library definition requires both
`WIIBRIDGE_WII_LIBRARY_PATH` and `WIIBRIDGE_GAMECUBE_LIBRARY_PATH`; it does not
require `WIIBRIDGE_LIBRARY_PATH`. Missing or empty dedicated paths are rejected.
The shared-root definition remains `compose.yaml`. The digest-pinned optimized
candidate is in [`compose.ghcr.yaml`](../deploy/truenas/compose.ghcr.yaml); use
the same dedicated paths and your existing credentials, certificates and
persistent directories. See [the performance audit](full-performance-audit.md)
for its exact build identity and hardware qualification limits.
