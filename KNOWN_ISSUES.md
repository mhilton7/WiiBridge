# Known issues

## Legacy Pi certificate lifetime

The published `0.1.0-rc.1` Pi first-boot script generated the device management
certificate with a 30-day validity period. The Host could reject an otherwise
exact pinned certificate after that date, preventing automatic Pi-manager
startup.

The source correction generates 100-year certificates and treats the exact DER
pin as the lifetime device identity regardless of encoded validity dates. Unit,
TLS pin-mismatch, full test, and static gates pass. A clean Host/firmware build,
publication, deployment, and runtime confirmation remain pending.

Status: `PENDING` — publish and deploy the corrected Host; rebuild firmware for
future Pi provisioning. Existing Pi private keys and pinned public certificates
do not need rotation solely because their encoded date passed.

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
