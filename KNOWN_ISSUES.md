# Known issues

## Library relocation and separate platform paths

A replaced ZFS mount can leave the saved `/library` identity different from
the current bind mount. Ordinary scans correctly preserve the old catalog,
but older Hosts have no way to accept an intentionally moved dataset. An old
GameCube generation can also incorrectly mark newly scanned paths unavailable.

The local correction adds separate Wii/GameCube roots, per-platform status and
rescans, and authenticated, confirmed location recovery. A complete scan and
validated catalog commit accept the replacement identity atomically; failed
scans preserve the catalog and save data. Moved GameCube sources can rebuild
without reactivating an outdated generation. See
[the configuration and recovery guide](docs/library-locations.md).

Status: `FEATURE_IMAGE_PUBLISHED` — source revision `2f8df9d`
passes full tests, static/race checks, GitHub image CI, and real read-only
container recovery/activation tests. Its commit-specific image is published
and independently verified for anonymous pulls at
`sha256:efbe790aeb51a6b635cbbe3fa4268d7dda071b0d5b583f6b19e03b7aa83d6d1c`.
PR #13 is a draft; main and the default release tag remain unchanged.
Installation on the operator's TrueNAS app remains pending.

## Legacy Pi certificate lifetime

The published `0.1.0-rc.1` Pi first-boot script generated the device management
certificate with a 30-day validity period. The Host could reject an otherwise
exact pinned certificate after that date, preventing automatic Pi-manager
startup.

The source correction generates 100-year certificates and treats the exact DER
pin as the lifetime device identity regardless of encoded validity dates. Unit,
TLS pin-mismatch, full test, and static gates pass. A clean Host/firmware build,
publication, deployment, and runtime confirmation remain pending.

Status: `PUBLISHED` — corrected Host revision `7fd0c3a` is available by
immutable GHCR digest. TrueNAS deployment confirmation remains pending; rebuild
firmware for future Pi provisioning. Existing Pi private keys and pinned public
certificates do not need rotation solely because their encoded date passed.

## USB Loader GX stalls after large-catalog enumeration

The exact-FSInfo repair is deployed and USB Loader GX now advances beyond
`Initializing USB devices`, confirming that the prior mount blocker is
resolved.

The next visible message, `Loading resources`, remains on screen while
USB Loader GX r1283 enters `MainMenu`, mounts the game partition, enumerates
`/wbfs`, and reads game headers before resuming the GUI. The physical catalog
read was finite and error-free: Pi NBD requests rose from 4,251 to 5,518 and
then stopped, Pi CPU returned below 1%, and NBD/USB error counters remained
zero.

The loader remains on that stale screen after the catalog traversal completes.
The current failure is therefore after WiiBridge transport and USB/FAT
mounting, while USB Loader GX processes the enumerated WBFS file set.

A read-only audit of the exact live export found 59 virtual segments sized
4,294,963,200 bytes (`4 GiB - 4 KiB`). On the deployed 32 KiB-cluster volume,
each segment's FAT chain spans exactly 4,294,967,296 bytes. That value wraps to
zero in 32-bit FAT chain-length accounting. Independent `fsck.fat -n`
reproduces the zero-length-chain failure on the live export and on a compact
synthetic fixture. USB Loader GX r1283 instead uses 4,294,934,528-byte
(`4 GiB - 32 KiB`) splits, explicitly keeping each part one cluster below the
boundary.

The builder now matches the loader boundary. The unchanged regression passes
independent `fsck.fat`, archive-only attribute checks, unique alias and LFN
checks, segment ordering, and exact banner-range/split-boundary source reads.
Full validation, clean publication, deployment, and physical retesting remain
pending; this is not yet a physical fix claim.

The Wii SD filesystem was dirty and its FAT copies differed. A full
allocated-file recovery archive plus raw MBR/boot/FAT metadata were retained
before repairing it. The loader caches were structurally valid, backed up, and
moved aside. A Wii-only, no-cache, disc-title, list-mode configuration is now
staged, with free-space and banner-cache work disabled.

Status: `PENDING` — publish and deploy the clean split-boundary correction,
then cold boot USB Loader GX with the repaired SD card and unchanged complete
source catalog.
