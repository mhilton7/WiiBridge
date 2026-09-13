# Known issues

## Current September performance candidate

[The optimized prerelease](https://github.com/mhilton7/WiiBridge/releases/tag/perf-2026-09-13-3c5dd91) passes complete software, firmware-offline
and local container/kernel validation. The previously pending firmware build
and publication work is complete. Exact identities and results are in
[the audit](docs/full-performance-audit.md).

Physical qualification remains `DEFERRED_HARDWARE_UNAVAILABLE`. The candidate
has now been written to the operator's Zero W SD card with
existing settings preserved, complete readback and safe eject verified. The
card has not yet been booted in the physical Pi or tested in repeated Wii
launches, and the candidate host deployment was not performed in this task.
The earlier isolated fast LEGO Star Wars launch does not establish
that other multi-minute loader stalls are fixed.

Actual TrueNAS ARC/pool behavior, Pi Wi-Fi/USB resets, loader IOS choices,
board boot times and emulated-save crash/reconnect behavior need physical
measurement. Deep validation still performs source I/O; cancellation waits for
an active storage syscall to return. GameCube backend reads retain their
existing lock and bounded descriptor cache. Emulated-save status still
validates the managed generation and reads backup metadata. Physical card
mode avoids that status work. No separate emulated-status endpoint benchmark
or independent byte-for-byte full rebuild is claimed.

The separate-path interpolation defect is corrected: the dedicated Compose
definition requires both console paths and rejects missing/empty values. The
shared-root definition remains available. The immutable GHCR definition now
uses current WiiBridge settings and the tested candidate image. Moved mounts
still need an explicit authenticated recovery confirmation.

The first parallel firmware attempt failed shared mount cleanup and was
rejected. Private namespaces and a mounted-tree deletion guard corrected the
builder; the fresh all-board retry and cleanup passed.

## Historical issues and observations from earlier revisions

The entries below preserve earlier investigation history. Their pending and
publication statements refer to those earlier snapshots, not this candidate.

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

Status: `LOCALLY_VALIDATED` — full tests, static checks, race checks, and a
real read-only container recovery/activation test pass. Publication and
installation of the updated Host image on TrueNAS remain pending.

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
# Full performance audit — current limitations

- The optimized release is still being implemented and is not yet published.
  Baseline validation at `cbb1d7842d866a7ba3969a2a8ae39679ccd5cd27` passed.
- Synthetic ext4 and loopback NBD measurements do not qualify real TrueNAS,
  Raspberry Pi, USB gadget, cIOS, or Wii launch performance. Those physical
  tests remain `DEFERRED_HARDWARE_UNAVAILABLE`.
- Initial exploratory Go benchmarks used RAM-backed temporary storage. Final
  comparisons use ext4 and explicitly identify source page-cache conditions.
- Baseline firmware packaging modifies tracked reports between target builds
  and hardcodes parts of provenance. Baseline evidence is retained as produced;
  optimized release provenance still requires correction and validation.
