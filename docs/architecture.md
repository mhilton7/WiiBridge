# Architecture and implementation decision

The host scanner records persistent filesystem identity where supported,
device/inode, size, nanosecond mtime, and split extents. Snapshot construction creates only FAT32 metadata and an extent index.
Every payload read revalidates source identity and opens the source read-only;
no whole payload is read or persisted.

The Wii FAT32 compatibility profile retains 32 KiB clusters for large volumes,
one genuine free cluster, exact matching FSInfo copies, and archive-only WBFS
entries. Virtual WBFS parts use USB Loader GX's `4 GiB - 32 KiB` boundary. A
larger `4 GiB - 4 KiB` part still fits a FAT directory size field, but rounds
to an exact 2^32-byte chain at 32 KiB clusters and overflows 32-bit chain-length
accounting. Payload extents remain direct views into the immutable source; the
boundary change does not copy or rewrite source data.

Go was selected for memory safety, bounded concurrency, a mature TLS stack,
small static amd64/ARM binaries, and shared host/Pi maintenance. SQLite uses a
pinned pure-Go driver, WAL transactions, foreign keys, schema migrations, and
atomic active-snapshot publication.

The NBD engine is a standalone fixed-newstyle implementation rather than an
nbdkit plugin. This avoids embedding a scripting runtime and permits the same
memory-safe, static container. It accepts only STARTTLS before export selection,
requires mutual X.509 authentication, bounds option/request sizes and deadlines,
supports read/flush, and rejects trim/write-zero. Wii and physical GameCube
reject all writes; emulated GameCube routes a complete write only through one
checksummed Host-generated save extent into the bounded save journal. NBD is pinned to
TLS 1.2 because the release host's libnbd 1.22/GnuTLS 3.8.9 combination fails
to select its client certificate for a Go TLS 1.3 CertificateRequest; HTTPS
remains TLS 1.3. Protocol and real parser fuzz tests cover framing, and both
libnbd and the Linux kernel NBD client are exercised independently.

The Pi uses the kernel NBD client with mandatory TLS flags. A typed privileged
helper exposes only fixed connect/disconnect, gadget, test/cache, and power
actions. It verifies target board and the selected export mode before ConfigFS
attachment. No SMB mount participates in the runtime path.

SQLite schema 2 adds last-complete Wii/GameCube catalog items, source-root
health, compatibility diagnostics, bounded source events, and performance
session summaries. Failed source preflight/traversal never executes destructive
reconciliation. `shared/compat`, `shared/sourcehealth`, `shared/contract`, and
`shared/perf` are the common contracts used by Host and firmware.

Routine performance observations are fixed atomic counters, histograms, and
rolling buckets. Pi system sampling is authenticated and cached. Only bounded
session summaries and low-frequency aggregate checkpoints reach persistent
storage; there is no disk or network call in the NBD observation path.
