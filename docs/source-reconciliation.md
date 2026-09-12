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
the configured absolute path with the strongest available mount/device
identity; container environments that hide mount details safely retain the
remaining fields.

Preflight uses only `lstat`, directory open/read, `statfs`, and mount metadata.
It never writes a probe file. It rejects a missing/non-directory/symlink root,
permission failure, changed device, changed known mount, and an unexpectedly
empty root after a prior non-empty successful scan.

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

Wii and GameCube backends verify stored size, mtime, device, and inode before
payload reads. A failed read returns an error; zero data or another file is
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
