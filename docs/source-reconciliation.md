# Source availability and catalog reconciliation

WiiBridge schema migration 2 separates source availability from catalog item
existence. A disconnected dataset is not an empty library.

Source states are `available`, `offline`, `unreachable`,
`permission-denied`, `authentication-failed`, `mount-missing`,
`temporarily-unavailable`, `changed`, `missing-confirmed`, `invalid`, and
`unknown`. Derived item availability is `playable`, `source-offline`,
`source-changed`, `validation-required`, `missing-confirmed`, or `invalid`;
validation and availability remain separate dashboard fields.

## Platform-specific locations

Wii and GameCube can use separate configured roots with independent source
states and rescans. Shared-root scans remain atomic across both catalogs.
An explicitly confirmed replacement location is committed only after a
complete successful scan and snapshot validation. See
[library locations](library-locations.md) for configuration and recovery.

## Source identity and scan transaction

SQLite stores source ID, configured root, device/filesystem/mount identity when
visible in the container, last successful/attempted scan, last successful item
count, failure code/message, and consecutive failures. The source ID combines
the configured absolute path with a persistent filesystem ID and directory
inode where supported, or the strict legacy mount/device identity otherwise.

Preflight uses only `lstat`, directory open/read, `statfs`, and mount metadata.
It never writes a probe file. It rejects a missing/non-directory/symlink root,
permission failure, changed filesystem or directory, and an unexpectedly empty
root after a prior non-empty successful scan. Legacy records also require their
original device and known mount until persistent identity is enrolled.

A scan has preflight, complete discovery, item validation, reconciliation, and
commit phases. Wii and GameCube traversal errors fail the whole source scan.
For a shared root, the rescan endpoint reconciles both catalogs in one SQLite
transaction only after both traversals complete. Separate roots are scanned and
committed independently, so an unavailable platform cannot block the other. A failed or partial scan updates bounded
diagnostics and serves the prior complete catalog with offline availability;
it never commits an empty snapshot, deletes generations, drops save
associations, or removes a GameCube validation receipt merely because a source
is offline.

On the first schema-2 startup, the migration transaction seeds the new Wii
catalog table from the authoritative active schema-1 snapshot. Until a real
source-root row is established, that snapshot's game count is also the safe
preflight baseline. An empty failed mount therefore cannot erase a legacy
catalog during the upgrade. An existing database is checkpointed first and
copied atomically to `wiibridge.sqlite3.pre-schema2.bak`; the migration never
overwrites a prior rollback backup.

After a complete available scan, an absent item becomes
`validation-required`. It becomes `missing-confirmed` only after two complete,
failure-free scans observe the same absence. The dashboard requires an
explicit CSRF-protected acknowledgement before removing that bounded tombstone.
A failed scan never increments missing observations.

## Active reads and reconnection

Wii and GameCube backends verify stored size, mtime, filesystem identity and inode
before payload reads, retaining device checks for legacy or unsupported sources.
A failed read returns an error; zero data or another file is
never substituted. The hot path increments bounded counters and queues one
rate-limited event (at most one per backend per 10 seconds). A background
worker performs source preflight and persistence, with an additional
30-second per-code persistence limit; the NBD read does not mutate the
catalog.

An offline GameCube source blocks affected activation/read but retains the
generation and its prior deep-validation receipt. Returning unchanged files
can reuse the trusted generation. A positively changed identity blocks the
generation and revokes its validation receipt until rebuild/deep validation.
Save cards remain in their independent managed directory in every source
state.

Stable codes are `SOURCE-OFFLINE`, `SOURCE-UNREACHABLE`,
`SOURCE-PERMISSION-DENIED`, `SOURCE-AUTHENTICATION-FAILED`,
`SOURCE-MOUNT-MISSING`, `SOURCE-IDENTITY-CHANGED`,
`SOURCE-PARTIAL-SCAN`, `SOURCE-MISSING-CONFIRMED`, and
`SOURCE-READ-FAILED`.

The dashboard provides source identity/state, scan times/counts, current
failure, affected games, retry/rescan, confirmed-removal acknowledgement, and
an authenticated bounded JSON diagnostic export. There is deliberately no
bulk removal action for offline games.

## Persistent filesystem identities and automatic return

Hosts advertising `stable-source-identity-v1` enroll the filesystem ID and
library directory inode after a successful scan. ZFS uses its dataset-derived
`statfs.f_fsid`; ext4 uses its UUID-derived `f_fsid`. Zero IDs and filesystems
without supported persistent identity semantics keep the strict legacy
mount/device checks. No probe or marker is written into a source directory.

Once enrolled, changing only the Linux device number or mount table identifiers
does not change library identity. A different filesystem, directory inode,
configured folder, missing path, unreadable source, symlink, or unexpectedly
empty previously populated source still fails closed. Cloned filesystems with
copied filesystem IDs are not cryptographically distinguishable by filesystem
metadata alone; the existing file metadata and content-validation guarantees
still apply. Runtime source reads retain inode, size and nanosecond mtime checks.

SQLite schema 3 adds identity columns without changing catalog or save rows.
An existing database is checkpointed and backed up to
`wiibridge.sqlite3.pre-schema3.bak` before the additive migration. Legacy records
are enrolled only when their original mount/device checks still pass. An
already mismatched legacy mount needs the existing explicit location recovery
once; a migration cannot safely invent historical filesystem identity.

New Wii snapshots and GameCube file maps contain filesystem IDs. For an old
validated GameCube generation, the host first verifies the old device, inode,
size and mtime and its existing deep-validation receipt, then atomically adds
filesystem IDs to that receipt. The original validation time, immutable layout,
FAT metadata, generation ID, content hashes and save layout remain unchanged.
The receipt is bound to the original complete source identity set. Subsequent
remounts reuse this generation and its validation instead of rehashing payloads.
Changed legacy sources cannot be enrolled; actual file changes still require
normal validation/rebuild. Unknown filesystems retain the older behavior.

Unavailable sources are probed after 5, 10 and 20 seconds and then every 30
seconds. Only a source matching its saved trust baseline proceeds to the normal
read-only scan and atomic catalog commit. The original catalog stays available
for display during an outage. A mismatched filesystem or directory is never
automatically accepted as a relocation. Once the original source is available,
readiness returns; any interrupted Pi/NBD session may still need reconnection.
A container with a permanently stale bind mount still needs its mount repaired
or the container recreated; the host cannot repair TrueNAS mounts from inside
its unprivileged container.

Validation covers simulated ZFS identities, ext4 file reads, legacy receipt and
SQLite migration, wrong filesystem/directory/file rejection, real read-only
container restarts, and a missing source returning without an operator rescan.
Actual TrueNAS reboot/pool export-import testing remains a hardware acceptance
step; synthetic device renumbering is not a claim that such a reboot was run.

Identity semantics are grounded in the
[OpenZFS statfs implementation](https://github.com/openzfs/zfs/blob/zfs-2.3.4/module/os/linux/zfs/zfs_vfsops.c#L1046)
and [ext4 statfs implementation](https://github.com/torvalds/linux/blob/v6.12/fs/ext4/super.c#L6415).
