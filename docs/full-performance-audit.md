# Full-system performance audit — September 2026

This audit starts from remote main at
`cbb1d7842d866a7ba3969a2a8ae39679ccd5cd27`, committed
2026-08-28T14:53:07-07:00. The repository is
https://github.com/mhilton7/WiiBridge.git. A fresh clone was fetched with pruning
and tags, checked out on main, and fast-forwarded before recording an empty
working-tree status. The performance branch is
`perf/full-system-optimization`. No operator-owned checkout was reset.

The separate-library implementation from
`2f8df9dd160c8cc8c0a6da081137674dea925988` was then cherry-picked as a
functional prerequisite. It adds independent Wii and GameCube roots and
explicit recovery after an intentional mount relocation. It was absent from
the pinned main baseline. Its extra dashboard controls are included in the
before/after comparison; they are not attributed to a performance optimization.

The application version remains `0.1.0-rc.1`; immutable source revisions and
image digests distinguish the optimized candidate. Final artifact identity and
validation are recorded in the release manifest accompanying this report.

## Environment and baseline

| Item | Investigation environment |
|---|---|
| Application Go toolchain | go1.25.12, linux/amd64 |
| OS | Debian GNU/Linux 13, kernel 6.12.107+deb13-amd64 |
| CPU allocation | 8 x86-64 virtual CPUs |
| RAM | 16,737,259,520 bytes |
| Measurement filesystem | ext4; explicit disk-backed TMPDIR |
| Container engine | Docker 26.1.5+dfsg1; Podman unavailable |
| Firmware tools | pi-gen, debootstrap, QEMU ARM/AArch64, bmaptool, dosfstools |
| Pi | Prior operator API identified Zero W; physical test access not established |
| Wii | Operator-owned console; launches require operator participation |
| TrueNAS | Existing operator deployment; no ZFS/management console audit access |

The complete local inventory also records the exposed CPU model and workspace
location. Public evidence omits operator machine fingerprints, private
addresses, credentials and local home/media paths.

Before application edits, all requested baseline checks passed:
`make test`, `make static`, `make server`, `make compose`, `make oci`,
the full server/Pi/shared/tests race suite, and server/Pi/shared vet.
`make release` also passed, including full Zero W, Pi 4 and Pi 5 builds and
all three offline firmware validations. The original source and artifacts
remain in the detached baseline workspace. See
[baseline validation](../reports/performance/2026-09-full-audit/baseline-validation.json).

The baseline release command took 4,033 seconds. That is an execution record,
not an isolated build-speed benchmark: other audit work overlapped the build.
QEMU checked application execution, not a physical board boot.

## Measurement method

The reusable [audit harness](../tests/performance/README.md) overlays identical
synthetic benchmark fixtures onto the baseline and optimized source. It covers
1, 1,000 and 10,000-title Wii mappings, GameCube metadata up to a 512 GiB
virtual volume (manager fixtures set a 512 GiB save reserve with three 2 MiB synthetic discs), SQLite reconciliation, scanners, dashboard rendering, metrics,
save overlays and persistent loopback mTLS NBD sessions.

Each canonical benchmark has six 750 ms samples, GOMAXPROCS=8, GOGC=100,
CGO_ENABLED=0 and the same Go toolchain. The timed runs used ext4 temporary
storage after the baseline firmware build finished. No firmware build
overlapped the measurements. Reported byte counts are measured allocations per
operation, not peak RSS.

The earlier exploratory run used the environment's tmpfs temporary directory.
Its wall times are excluded from the comparison. In particular, durable save
writes measured on tmpfs must not be used as disk-backed flush timings.

The independent latency fixture writes allocated files and syncs them before
testing. Each evicted sample uses POSIX_FADV_DONTNEED and mincore to verify the
queried source pages are absent from the Linux page cache. Hypervisor, device
and storage-controller caches remain uncontrolled. Warm tests deliberately use
the Linux source page cache. Neither case represents TrueNAS ZFS ARC or Wii
launch latency.

[Raw normalized samples](../reports/performance/2026-09-full-audit/baseline.txt),
[optimized samples](../reports/performance/2026-09-full-audit/optimized.txt) and
[benchstat](../reports/performance/2026-09-full-audit/benchstat.txt) retain all
cases. One baseline NBD result line was interrupted by an asynchronous
connection-close warning. Its adjacent numeric continuation was rejoined
without changing the measurement; all six samples remain. This is recorded in
[normalization notes](../reports/performance/2026-09-full-audit/normalization.json).
The unedited original log is retained locally.

## Measured results

Values below are medians of six benchmark samples. They describe the named
software workload, not the complete console path.

| Workload | Baseline | Optimized |
|---|---:|---:|
| Wii 10,000-title mapping, 4 KiB read | 132.66 µs; 5,005 allocations | 6.49 µs; 5 allocations |
| Wii 10,000-title mapping, 64 KiB read | 133.69 µs | 7.61 µs |
| Wii synthesized FAT, 1 MiB read | 1.467 ms; 1 MiB allocated | 0.216 ms; zero allocation |
| GameCube 512 GiB generation fast validation | 137.02 ms; 269.27 MB allocated | 34.13 ms; 67.41 MB allocated |
| GameCube 512 GiB active-generation lookup | 276.97 ms; 538.55 MB allocated | 34.36 ms; 67.42 MB allocated |
| GameCube 512 GiB backend open | 230.68 ms; 538.53 MB allocated | 69.46 ms; 134.79 MB allocated |
| GameCube 512 GiB layout construction | 83.58 ms; 432.42 MB allocated | 31.79 ms; 134.43 MB allocated |
| GameCube 512 GiB stored metadata | 134,318,592 bytes | 67,209,728 bytes |
| Clean save-card 1 MiB read, including byte comparison | 858.44 µs | 41.57 µs |
| Mixed clean/dirty save-card 1 MiB read | 834.27 µs | 61.63 µs |
| SQLite reconciliation, 10,000 items | 98.17 ms | 45.09 ms |
| mTLS NBD 4 KiB request, loopback | 27.03 µs | 13.03 µs |
| mTLS NBD 64 KiB request, loopback | 59.42 µs | 50.64 µs |
| Dashboard with 10,000 Wii titles | 37.97 ms; 1,555,288 HTML bytes | 1.27 ms; 33,250 HTML bytes |
| Dashboard with pending 512 GiB GameCube validation | 137.10 ms; 269.28 MB allocated | 0.118 ms; 75,972 bytes allocated |
| Background-tab polling, no active build | 44 requests/minute | zero new periodic requests |

The 4 KiB TLS reply requires one underlying TLS transport write instead of
four and carries 4,141 encrypted bytes instead of 4,228. The 64 KiB case uses
five writes instead of seven; the 1 MiB case uses 65 instead of 67. Counts
exclude TCP/IP overhead and handshake traffic. TLS 1.2 was selected for the
controlled benchmark to match the NBD compatibility path; cipher and trust
policy were not weakened.

The 10,000-title warm 4 KiB latency distribution improved from 72.75/394.16/
758.69 µs median/p95/p99 to 6.18/16.70/32.99 µs. With verified source-page
eviction, the corresponding values were 273.46/513.73/1,737.39 µs and
217.43/298.43/428.25 µs. These are single distribution runs with 1,000 warm
and 200 evicted samples per case. Tail values are sensitive to scheduling and
storage; they are not six-run confidence intervals.

All results, including less favorable ones, are retained. The 100-title
dashboard grew by 2,413 HTML bytes and took about 4% longer, with the new
separate-source controls included. The scanners showed roughly 2–3% longer
times with unchanged allocation counts; their scan paths were not optimized.
Cached Pi metrics and host summary serialization remained similar. The
durable 4 KiB save-write workload stayed around 0.94–0.97 ms: journaling and
sync were preserved. Small changes in unchanged paths are not evidence of a
new optimization. No aggregate speedup across unrelated workloads is claimed.

The supplemental healthy GameCube startup benchmark reopens a manager with an
existing validated generation, including its receipt and source checks. Its
six-sample results are included with the main evidence. For the 512 GiB reserve
fixture, allocated memory falls from about 538.57 MB to 67.44 MB per reopen; time falls from 266.50 ms to 32.20 ms.

A fixed strace workload verified the save I/O reduction independently of timing.
After subtracting two process-startup preads, 100 unaligned 1 MiB clean reads
used 204,900 preads on baseline and 100 after optimization. The mixed workload
used 192,000 and 1,600 respectively. No durability operation was removed.

The kernel GameCube free-space check waits for mount/device-discovery traffic
to settle, then queries statfs on a read-only mount with usefree. Both revisions
reported 261,980 free clusters. Baseline required 655,360 additional NBD payload
bytes in three requests; optimized required zero. Some FAT bytes were already
cached by mounting. Earlier un-settled diagnostic counts mixed in asynchronous
mount reads and are not used for this comparison. Both revisions passed fsck
without repairs and complete synthetic payload checksum validation.

## Engineering review and changes

| Area | Finding and disposition |
|---|---|
| TrueNAS source storage | Kept mandatory read-only mounts, source identity checks and explicit relocation recovery. Actual ZFS ARC, pool latency, disk sleep and dataset fragmentation require hardware access. |
| Wii synthetic FAT/WBFS | Replaced linear payload-extent lookup and escaping range-variable copies with binary search over immutable ordered extents. Synthesized FAT sectors directly into the destination or stack scratch space and searched chains once per sector. |
| GameCube metadata/startup | Removed temporary backend creation used only for validation, repeated metadata reads and duplicate healthy-startup validation. Hashes, extent checks, configured-root checks, receipts and defensive backend copies remain. |
| GameCube FAT32 storage | Stored one immutable FAT byte array behind both virtual FAT extents. Schema 2 already permits shared storage offsets. Both virtual copies remain readable and equal. FSInfo now records the exact free count and next-free hint. |
| Mutual-TLS NBD | Decoded fixed request headers in one bounded read and framed reply header plus payload in one pooled buffer. Rejected short backend reads even when a backend incorrectly returns a nil error. Preserved request limits, read-only errors, deadlines and failure accounting. |
| Linux NBD/network | Exercised actual libnbd and kernel clients against the OCI image. Did not change network buffers, cipher selection, read-ahead or request concurrency without physical measurements. |
| Pi controller and metrics | Status/metrics are already cached and polled centrally. No speculative cache or polling rewrite. Pi userspace cache tests do not imply that cache is in the active Linux NBD data path. |
| USB gadget/Wii loader | Kept detach/reconnect safeguards and the existing split-file geometry fixes. Physical USB resets, IOS reloads and loader-specific mount behavior remain separate qualification work. |
| Save overlay | Coalesced adjacent clean blocks into file reads while dirty bytes retain precedence. Journal append, sync, recovery, checksums, checkpoints, backups, locking and write boundaries remain unchanged. |
| Library scanning/reconciliation | Reused prepared SQL statements inside each existing transaction and checked row-iteration errors. Complete-scan atomicity, preserved catalogs and two-observation removal confirmation remain. |
| Dashboard | Added 100-row pages with total counts, independent page links and full-catalog search. Used a display-only cached generation ID instead of rereading large metadata for pending generations. Activation still revalidates. |
| Browser polling | Suppressed new periodic requests while hidden and overlapping periodic requests per panel; refreshed on visibility return. Foreground polling intervals remain unchanged. |
| Background validation/builds | Propagated cancellation through bounded 32 KiB hash reads and FST traversal, checked cancellation before success/promotion, and stopped mutating the caller's disc slice while fingerprinting. Deep source validation was retained. |
| Firmware services | Reviewed network-online dependencies, auto-attach conditions/retry intervals, recovery detach, restricted privileged helpers and gadget setup. Kept these safeguards; QEMU does not provide a physical boot profile. |
| Release tooling | Replaced fixed SBOM dates and hardcoded dirty provenance with captured build inputs. Packaging checks source revision/content, generated reports default to ignored build storage, and offline firmware validation compares the embedded controller with its board build. Concurrent builders use private mount namespaces and refuse cleanup of mounted trees. |
| Benchmark tooling | Fixed the older allocated-WBFS benchmark that recreated and truncated its fixture, and corrected the older performance-report command's misleading cold-cache label and microsecond truncation. That changed fixture is excluded from the before/after table. |

FSInfo can avoid a full FAT walk when a client trusts its free-space value.
Linux's implementation checks the cached valid count before scanning:
[Linux v6.12 FAT free-space implementation](https://github.com/torvalds/linux/blob/v6.12/fs/fat/fatent.c#L670).
This is a client-dependent benefit. Inspection of the pinned Nintendont source
found no application call to `f_getfree`; no Nintendont launch improvement is
claimed from FSInfo alone.

## Correctness and deployment limits

The unchanged baseline generation was opened by the optimized code with its
metadata hashes preserved and its payload reads compared independently against
the synthetic source files. Newly built generations expose identical FAT
copies while storing fewer bytes. Tampered metadata still fails validation
and the next backend open. Read tests cover extent boundaries, holes, padding,
unaligned requests, both FAT copies and end-of-disk bounds. NBD tests cover
truncated headers, short backend reads, pooled-buffer reuse, partial replies,
write failures and balanced metrics.

The kernel integration mounts synthetic exports read-only, verifies their
payload hashes, rejects a block write and runs fsck without repairs. Separate
library integration covers moved GameCube files, intentionally replaced
mounts, empty replacement preservation, and GameCube activation while Wii
storage is unavailable. These tests use local ext4/Docker, not TrueNAS.

Existing GameCube generations do not need automatic rewriting. Validation
and read-path improvements apply on upgrade; compact FAT storage and exact
FSInfo apply to newly built generations. Retain the prior host image digest,
the data/config backup and the current generation for rollback. An intentional
source relocation must still be explicitly accepted through the authenticated
recovery control after checking the configured read-only mount.

Cancellation is cooperative: it stops between bounded storage reads, but
cannot interrupt a storage syscall that has not returned. Full source hashing
still consumes I/O during a requested build or deep validation. GameCube
backend reads still use the existing lock and bounded descriptor cache.

Physical qualification remains `DEFERRED_HARDWARE_UNAVAILABLE`: cold and warm
launches across several Wii/GameCube titles; USB Loader GX IOS selection;
Zero W Wi-Fi versus wired Pi networking; USB enumeration/reset traces;
TrueNAS source-read latency and ARC state; Pi CPU, temperature, memory and
NBD timing; emulated-save crash/reconnect recovery; and first boot on each
supported board. Record simultaneous host/Pi timestamps and repeat A/B launches
with identical loader settings and cache conditions.

The operator's earlier report of one near-instant LEGO Star Wars launch after
an IOS-setting change is useful context, but is not a controlled performance
result or proof that every multi-minute black screen has been corrected.
The required release qualification is
**SOFTWARE-COMPLETE RELEASE CANDIDATE — HARDWARE UNVERIFIED**.

The initial concurrent optimized firmware attempt exposed shared pi-gen chroot
mount propagation: Pi 4/Pi 5 unmount cleanup failed while the Zero W build was
still active. The attempt was rejected, its remaining builder stopped, and all
audit mounts removed normally. The original host mount topology was restored.
The correction isolates each pi-gen job in a private mount namespace and
refuses build-tree deletion if any mount remains below it. This build-only
correction does not change the benchmarked host/Pi runtime code.
