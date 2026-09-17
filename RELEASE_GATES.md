# Release gates

## Stable source identity gates — 2026-09-17

| Gate | Status | Evidence |
| --- | --- | --- |
| Persistent source identity, migration and read guards | PASS | Simulated ZFS identity and real ext4 Wii/GameCube tests; changed filesystem/directory/file rejected |
| Existing GameCube generation enrollment | PASS | Prior receipt time and immutable generation files unchanged; remount reads succeed |
| Automatic source return | PASS | Real read-only container observes missing source returning without an operator rescan; prior catalog preserved |
| Full software validation | PASS | make test, make static, full race suite, Compose parsing and ARMv6 controller cross-build |
| Main and immutable GHCR publication | PASS | PR 15 merged; main CI passed; anonymously resolved digest and pulled clean binary match main revision |
| TrueNAS reboot/pool export-import and physical gameplay | DEFERRED_HARDWARE_UNAVAILABLE | Synthetic device renumbering and container tests do not establish actual appliance behavior |
| Published image and generic YAML | PASS | Downloaded binary passes all eight container recovery checks; local-only generic YAML passes Compose parsing |

Current evidence: `reports/truenas/stable-source-identity-20260917.json`.
The sections below retain historical release observations.

## Previous September performance candidate

**SOFTWARE-COMPLETE RELEASE CANDIDATE — HARDWARE UNVERIFIED**

[Published prerelease](https://github.com/mhilton7/WiiBridge/releases/tag/perf-2026-09-13-3c5dd91) from clean source
`3c5dd917cfe0d6771cdd2e003fa9002dfcb963f2`, compared with fresh main
`cbb1d7842d866a7ba3969a2a8ae39679ccd5cd27`. The [audit](docs/full-performance-audit.md),
[manifest](reports/performance/2026-09-full-audit/release-manifest.json) and
[publication verification](reports/performance/2026-09-full-audit/release-publication.json)
record measurements, exact artifacts and limits.

| Current gate | Status | Evidence |
|---|---|---|
| Fresh main baseline and complete baseline release | PASS | Preserved baseline source/artifacts and validation record |
| Complete tests, static checks, race detector and vet | PASS | Final clean source validation |
| Server, OCI and Compose | PASS | Final digest matches tested container; all three Compose definitions parse |
| Wii/GameCube Linux NBD and libnbd mTLS | PASS | Readonly mounts, source hashes, both FAT copies, denied writes, fsck without repairs |
| Source relocation and independent console paths | PASS | Real readonly container recovery/activation and missing-path parser checks |
| Existing GameCube generation compatibility | PASS | Preserved baseline metadata and source-byte checks |
| Zero W, Pi 4 and Pi 5 complete firmware builds | PASS | Fresh successful retry with private mount namespaces |
| All-board offline validation and controller identity | PASS | Filesystems, boot/module/service checks, QEMU smoke and embedded binary match |
| Compression, checksums, SBOM and provenance | PASS | Full decompressed hashes and captured clean build inputs |
| Immutable GHCR publication and GitHub release assets | PASS | Published digest plus all 30 remote sizes/hashes verified |
| Independent byte-for-byte build reproducibility | PENDING | Not performed; unsigned local provenance is not an independent attestation |
| Zero W operator SD flash, whole-image readback and safe eject | PASS | [Card verification](reports/firmware/zero-w-armhf/performance-card-flash-2026-09-13.json) |
| Physical board boot, Wii launches, TrueNAS/ZFS performance and save recovery | DEFERRED_HARDWARE_UNAVAILABLE | Requires operator hardware qualification |

The failed initial parallel firmware attempt was rejected and cleaned. The
corrected all-board retry passed. Deployment YAML and publication records were
finalized after binary packaging; the manifest retains the exact clean binary
source revision. No operator TrueNAS/Pi deployment was performed.

## Historical gates from earlier revisions

The entries below describe earlier work and do not qualify this candidate.

Status values are `PENDING`, `PASS`, `FAIL`, `BLOCKED_EXTERNAL`, and
`DEFERRED_HARDWARE_UNAVAILABLE`. Only executed checks may be marked `PASS`.

| Gate | Status | Evidence |
|---|---|---|
| Reproducible clean build | PENDING | Clean board builds passed; a second byte-for-byte full rebuild was not performed |
| Dependencies and revisions pinned | PASS | `versions.lock`, `go.sum` |
| Unit, security, and static analysis | PASS | `make test`, `make static` |
| FAT32 independent validation | PASS | `tests/fat32/fsck_test.go` |
| NBD mutual-TLS interoperability | PASS | `reports/libnbd-info.json`, kernel-client test |
| Mutation and failure injection | PASS | `tests/unit/scanner_test.go`, NBD protocol tests |
| Bounded cache | PASS | `tests/unit/cache_test.go` |
| 1,000-entry catalog | PASS | `tests/integration/catalog1000_test.go` |
| Server/container package | PASS | `dist/wiibridge-host-0.1.0-rc.1.oci` |
| Hardened unprivileged container | PASS | `reports/container-test.json` |
| TrueNAS Compose validation | PASS | independent Docker Compose parser |
| TrueNAS external/loopback TLS health-check compatibility | PASS | Reissued same-CA server certificate covers the private TrueNAS address and `127.0.0.1`; the packaged host's built-in health-check command passed against a local instance using the replacement bundle |
| TrueNAS live deployment | PASS | Strict HTTPS verification accepts the replacement CA/IP certificate, and the live NBD export accepts the generated client identity, reports read-only, and returns a 1,284,936,704-byte virtual disk |
| Zero W original image offline validation | PASS | `reports/firmware/zero-w-armhf/`; offline checks did not detect the later physical setup failures |
| Zero W repaired source/card offline validation | PASS | `make test`, `make static`, ARMv6 QEMU smoke, live-card service-identity/controller/detach checks |
| Zero W repaired image rebuild | PASS | Clean current-source pi-gen build, offline firmware validation, hashes, bmap, SBOM, and provenance completed 2026-07-25 UTC |
| Zero W repaired image physical flash/readback | PASS | All 163 allocated bmap ranges match the source image; flashed FAT/ext4, services, clean identity, controller binary, and dnsmasq-user checks pass |
| Pi 4 image offline validation | PASS | `reports/firmware/pi4-arm64/` |
| Pi 5 image offline validation | PASS | `reports/firmware/pi5-arm64/` |
| Pi 4/Pi 5 artifact independence | PASS | Distinct whole-image SHA-256 values |
| Artifact checksums/SBOM/provenance | PASS | Per-board files under `dist/` |
| Physical Zero W initial boot | FAIL | Original rc.1 boot exposed failed recovery service and missing setup AP |
| Physical Zero W first repaired-card retest | FAIL | Recovery/controller passed; NetworkManager/wpa_supplicant AP failed to install WPA key on BCM43430 |
| Physical Zero W hostapd-card retest | FAIL | WPA2 handshake passed; dnsmasq lease file was blocked by service filesystem hardening |
| Physical Zero W DHCP-runtime repair retest | FAIL | WPA2 association passed, but dnsmasq could not read its configuration inside the protected credential directory; the client received a link-local address |
| Physical Zero W DHCP-config-permission repair retest | PASS | iPhone received DHCP and loaded the controller HTTPS login page |
| Physical Zero W fresh-image first boot | PASS | Root expansion and identity generation completed; recovery, AP, hostapd, dnsmasq, and controller started; hostapd reached `AP-ENABLED` and dnsmasq opened the `10.77.0.20`-`10.77.0.100` DHCP range |
| Physical Zero W 12-character management-login retest | PASS | The authenticated network-setup form was reached and submitted with the fresh image's 12-character management credential |
| Physical Zero W client Wi-Fi provisioning retest | PASS | The repaired provisioning form saved the 2.4 GHz client profile, and the Pi joined the configured home network after reboot |
| Retained-WiFi save and USB automation source validation | PASS | Optional Wi-Fi update tests, CSRF-protected dashboard actions, bounded opt-in auto-attach unit, systemd verification, full automated suite, ARMv6 build, and QEMU smoke pass |
| Physical Zero W revised server/USB UI retest | FAIL | Server connect reached the typed helper, but its `modprobe nbd` inherited `ProtectKernelModules=yes` and saw the intentionally hidden `/lib/modules` tree |
| NBD boot-preload sandbox repair source validation | PASS | Actual v6 image contains matching `nbd.ko.xz`; boot preload/module options, protected helper checks, systemd ordering, unit/static tests, and strengthened firmware validation pass |
| Physical Zero W NBD boot-preload repair card installation | PASS | Matching v6 `nbd.ko.xz`, preload/options, helper, and service graph verified on the physical card; credentials, Wi-Fi, TLS, bridge state, and machine identity preserved; post-repair filesystems clean |
| Physical Zero W NBD boot-preload repair retest | PASS | Rebooted Pi advanced through NBD setup to TLS negotiation without the prior module error |
| Generated client identity against live TrueNAS NBD | PASS | Local known-good Pi bundle completes mutual TLS to the private TrueNAS endpoint, export `all` is read-only and reports 1,284,936,704 bytes |
| Physical Zero W installed client credential load | FAIL | Pi `nbd-client` reports that `/etc/wiibridge/client.crt` or `client.key` cannot be loaded; direct known-good file replacement is pending |
| Physical Zero W client TLS and safe-poweroff repair installation | PASS | All three prior Pi TLS files were zero bytes; exact live-verified bundle, stronger provisioning helper, ARMv6 controller, typed safe-poweroff action, and restricted sudo rule installed; unrelated state preserved and filesystems clean |
| Physical Zero W repaired client TLS retest | PASS | Pi loaded the repaired credential, completed TLS negotiation, and received the expected 1,225 MB export size |
| Physical Zero W first NBD kernel connection | PASS | Persistent journal shows `Connected /dev/nbd0`, the correct capacity, and partition `p1` |
| Physical Zero W NBD idle survival | PASS | After a clean reboot the dashboard remained NBD-connected beyond the prior failure interval while the Pi controller stayed reachable for a monitored 90 seconds |
| Server NBD idle-session repair | PASS | Transmission deadline cleared after negotiation; idle regression, full tests, build, and hardened container lifecycle pass |
| Pi stale-device/test/VID-PID export repair installation | PASS | Updated helper and auto-attach unit exactly installed; authorized pair, Wi-Fi, TLS, bridge state, and identity preserved; filesystems clean |
| TrueNAS idlefix live deployment | PASS | GHCR image is deployed; strict HTTPS, mutual-TLS NBD, exact 1,284,936,704-byte size, and a read after 35 idle seconds pass |
| Physical Zero W repaired USB attach/Windows read | FAIL | Windows enumerated the gadget and the Pi reported USB link `configured`, but Windows requested formatting because the FAT32 BPB had zero legacy geometry fields |
| Windows FAT32 BPB geometry repair source validation | PASS | BPB now records 63 sectors/track, 255 heads, and 2,048 hidden sectors; `fsck.vfat`, independent `mtools` directory read, unit, static, and whitespace checks pass |
| TrueNAS Windows FAT32 replacement deployment | PASS | Live idlefix.2 export reports 63/255/2,048 BPB geometry and independent `mtools` reads both the root and `/wbfs` directories |
| Physical Windows idlefix.2 corrected FAT32 mount | FAIL | Windows still requested formatting; card journal shows continuous NBD/USB service with no host-read errors, while the fixed, read-only MBR disk had signature `0x00000000` |
| Windows fixed-disk identity repair source validation | PASS | Deterministic nonzero per-catalog MBR signature and matching FAT volume ID; unit, FAT, static, and whitespace checks pass |
| Pi partition-readiness repair installation | PASS | Replaced global `udevadm settle` with bounded `nbd0p1` readiness and direct disk/partition reads; exact helper installed, state preserved, filesystems clean |
| TrueNAS idlefix.3 live deployment | PASS | Nonzero MBR signature, matching FAT volume ID, 63/255/2,048 geometry, `mtools`, `fsck.fat`, and kernel read-only FAT mount all pass against the live export |
| Physical Windows idlefix.3 partition recognition | PASS | Operator reports that Windows detects and visually presents the partition; an explicit File Explorer `/wbfs` read remains to be captured |
| Physical Pi 4/Pi 5 boot | DEFERRED_HARDWARE_UNAVAILABLE | |
| Physical USB gadget enumeration | DEFERRED_HARDWARE_UNAVAILABLE | |
| Separate Linux USB-host test | DEFERRED_HARDWARE_UNAVAILABLE | |
| Nintendo Wii detection | PASS | Wii and USB Loader GX mount the virtual storage and display its game file |
| USB Loader GX enumeration | PASS | Loader reaches the catalog and displays the game file; post-failure Pi status remains NBD-connected, USB-attached, and configured |
| Original single WBFS payload and FAT extent validation | PASS | The earlier one-file live export contained one contiguous WBFS extent; Wiimms ISO Tool 3.01a verified both update and data partitions as OK |
| Current four-file catalog integrity | FAIL | The live export is now 3,619,423,232 bytes with four files and no FAT free space; three files pass Wiimms ISO Tool verification and one fails an H0 data-partition integrity check |
| Wii cIOS no-stall compatibility retest | FAIL | With endpoint stalls disabled, USB Loader GX still returned to the Wii System Menu on game boot; the experiment has been reverted |
| First targeted cIOS-handoff trace capture | FAIL | Wii reboot reproduced, but repeated tracefs function-list scans consumed 78 seconds of Pi Zero CPU and the capture file remained empty |
| Corrected targeted cIOS-handoff trace capture | PASS | 1,749 trace lines capture the real launch; NBD requests complete, no real-session block error occurs, and five host writes take the mass-storage function's immediate write-protected path |
| Physical strict read-only LUN cIOS compatibility | FAIL | The final rejected host write preceded the return-to-menu reset by about 596 ms; the controlled writable-overlay comparison rejected SCSI-level write protection alone, while DOS read-only file attributes remained unchanged |
| Physical volatile COW write-compatibility retest | FAIL | The overlay accepted all five host writes and every one of 117 NBD requests completed, but game launch still returned to the System Menu; the experiment was reverted |
| Attempted legal-game payload integrity | PASS | The operator identified 10 Minute Solution; its catalog entry is one of the three current files that passes full Wiimms ISO Tool verification |
| Wii d2x slot/base inventory | PASS | SysCheck HDE reports d2x-v11-beta3 at 248[38], 249[56], 250[57], and 251[58] |
| Controlled game IOS250/base57 configuration | PASS | Only the attempted game's USB Loader GX record was changed from inherited 249/base56 to explicit 250/base57; system IOS installations were not modified |
| Wii SD post-configuration integrity | PASS | Reconciled differing redundant FAT copies, then `fsck.vfat -n` passed cleanly and the IOS250 game setting and SysCheck report were read back from a read-only remount |
| Physical IOS250/base57 game launch | FAIL | The verified payload returned to the Wii System Menu exactly as it did through IOS249/base56 |
| Physical IOS251/base58 game launch | FAIL | The verified payload again returned to the Wii System Menu; all installed d2x USB bases now fail identically |
| Attempted-game boot-file extraction | PASS | Fresh live-export copy has contiguous FAT allocation, valid IMET/U8 banner archive with banner/icon/sound entries, valid main DOL, IOS53 TMD, and both partitions pass verification |
| Second verified high-LBA title banner/launch | FAIL | American Mensa Academy also has a blank banner and returns to the System Menu after a tiled Wii-logo framebuffer flash |
| Verified low-LBA title banner/launch | FAIL | The Squeakquel below 2 GiB also has a blank banner and returns to the System Menu, eliminating disk offset as the differentiator |
| USB Loader GX WBFS open-mode diagnosis | PASS | Official r1283 source enumerates headers read-only but opens WBFS files with `O_RDWR` for banner and boot access; all live WBFS FAT entries carry DOS read-only attribute `0x01` |
| FAT WBFS attribute compatibility repair | PASS | Builder emits archive `0x20` without read-only `0x01`; byte-level and independent `mattrib` regressions, full tests/static checks, FAT validation, and hardened container lifecycle pass while NBD/LUN remain read-only |
| GHCR WBFS attribute replacement image | PASS | Commit `74dfb5b`; `ghcr.io/OWNER/wiibridge-host:0.1.0-rc.1-wbfsattrfix.1@sha256:37d94d0c3f11ae8c33f96490fe9c0902b6b417907afd56a14d8dd370f4b2fe80` published and anonymously resolved |
| TrueNAS WBFS attribute replacement deployment | PASS | Strict HTTPS health passes; the 3,619,423,232-byte mutual-TLS NBD export remains read-only, and independent live `mattrib` inspection reports archive `A` without read-only `R` on all four WBFS entries |
| Physical banner extraction after attribute repair | PASS | USB Loader GX now displays the 10 Minute Solution banner through the repaired live export |
| Legal game launch | PASS | 10 Minute Solution successfully loads on the Wii through the Zero W bridge and repaired TrueNAS export |
| Gameplay soak | PENDING | Sustained gameplay plus a later power/reconnect cycle remain to be exercised |
| Cable, power, reconnect, reboot | PASS | Repeated reboot, NBD reconnect, USB reset, cIOS handoff, and trace captures completed without Pi transport failure |
| GameCube schema-2 no-copy generation | PASS | Complete FAT32 metadata and sorted source extents are generated without `library.img` or payload-copy helper calls |
| GameCube no-copy physical storage | PASS | 6,291,456 source bytes produced 2,252,800 physically allocated metadata bytes for an 8,589,934,592-byte apparent disk; retained-generation regression passes |
| GameCube source integrity and read-through | PASS | ISO/GCM/CISO/two-disc/FST reads match original sources; size, hash, symlink, escape, overlap, duplicate-path, and changed-FST checks pass |
| GameCube physical memory-card mode | PASS | Backend and NBD profile are read-only and every write is rejected |
| GameCube emulated memory-card overlay | PASS | Individual/shared raw cards, bounded block journal, immutable writable extents, crash recovery, transactional backup/restore/upload/download, physical-mode regression, NBD write routing, security, concurrency, and race tests pass; no payload image is copied |
| Host/Pi protocol and capability negotiation | PASS | Versioned authenticated descriptors, protocol overlap, board/device checks, operation-specific fresh authorization, cached-display-only results, unavailable/malformed/older-firmware tests, and ARMv6 controller build pass |
| Offline-source reconciliation | PASS | Schema-2 source health migration, complete-scan transaction, two-scan missing confirmation, failed/partial scan preservation, reconnect/change handling, source diagnostics, NBD failure isolation, and persistence tests pass |
| Bounded end-to-end performance dashboard | PASS | Fixed counters/rings/histograms, Host/NBD/source/save/Pi/USB views, bounded sessions, authenticated JSON/CSV APIs, graceful degradation, warnings, serialization and retention tests pass |
| Performance telemetry overhead | PASS | `reports/four-feature-performance.json`: 64 KiB GameCube backend read median 3,099 ns disabled / 3,239 ns enabled (+140 ns, 4.52%); no added allocations, request-path disk writes, hot-path network calls, or high-cardinality labels |
| Four-feature hardened OCI package | PASS | Clean implementation commit `7097d8a`: `wiibridge-host:0.1.0-rc.1@sha256:f34bd8d2036d918dd2ab841e53cbf6b924369c8dfbaa4b375bc1f142fe2b0d4e` |
| Four-feature Pi Zero W controller build | PASS | Clean current commit `c60d4b5` cross-builds as a stripped, statically linked ARM EABI5 executable; SHA-256 `c380f33b36f6408c46619e81f07585fe1b2f7e77e5a4225c5736988618b02b01` |
| Current four-feature Pi Zero W full image | PASS | Clean-source pi-gen build at `c60d4b5`; partition/fsck, board, boot, service, NBD, USB gadget module, QEMU smoke, no-payload, and no-embedded-identity checks pass. Image SHA-256 `89167db6b8dcd7e544589e90306f36e42c9066779430b5964ef19073f6a3f8e5`; XZ SHA-256 `2873476d07fa63f5b3b3ef25cf71379b0179322297f1a1915aca860a765a3e73` |
| Physical GameCube emulated-save acceptance | DEFERRED_HARDWARE_UNAVAILABLE | Host, protocol, and FAT/NBD tests do not prove Nintendont save behavior on a physical Wii |
| Physical Pi Zero W telemetry overhead | DEFERRED_HARDWARE_UNAVAILABLE | Current full image `89167db6…` is built and offline-validated, but has not been flashed or benchmarked on the Pi Zero W |
| Wii synthetic disk regression | PASS | FAT32, integration, unit, and race suites pass after replacing resident FAT storage with byte-compatible on-demand synthesis |
| Physical GameCube Wii/Nintendont acceptance | DEFERRED_HARDWARE_UNAVAILABLE | Host-side FAT32 and storage behavior pass; no claim of physical compatibility is made |
| TrueNAS multi-terabyte Wii FAT memory | PASS | 8 GiB fixture retains 5 chain descriptors and 10,752 non-FAT metadata bytes instead of at least 16,778,240 resident raw FAT bytes; representation scales with files rather than clusters |
| Live 1.65 TiB Wii export geometry diagnosis | PASS | The deployed 1,816,313,603,072-byte export uses 8 sectors/cluster and approximately 442.6 million data clusters, crossing into FAT32's reserved cluster-number range; the Pi remained configured with zero NBD failures while USB Loader GX stopped issuing reads |
| Adaptive large Wii FAT32 geometry | PASS | The first repair retains 4 KiB clusters where valid and selects a larger valid power-of-two cluster size subject to FAT32 cluster-number and MBR/LBA limits; the 1.65 TiB-scale regression selected 8 KiB and approximately 221 million data clusters |
| Adaptive large Wii TrueNAS deployment | PASS | Published commit `a0b2023`; the deployed 1,814,543,805,440-byte export is valid FAT32 with 8 KiB clusters, approximately 221 million data clusters, readable root and `/wbfs` directories, and healthy Host/Pi/NBD/USB status |
| Physical USB Loader GX valid-8-KiB large-library retest | FAIL | USB Loader GX still froze at `Initializing USB devices`; the Pi remained ready, NBD-connected, USB configured, and error-free, while completed NBD requests remained at 60 for a 22-second sample |
| Large Wii 32 KiB cluster policy | PASS | Libraries at or above 32 GiB now use the conventional Wii large-volume 32 KiB cluster geometry; the 1.65 TiB-scale regression exposes approximately 55.3 million data clusters and a 432,182-sector FAT per copy, while an 8 GiB regression retains 4 KiB clusters |
| Large Wii 32 KiB replacement OCI | PASS | Commit `2a7383a`; `ghcr.io/OWNER/wiibridge-host@sha256:5538addc6e525d0ffac405192c97a3ef955eef4d9974e327e22e3e070f487b3f` published and anonymously resolved |
| Large Wii 32 KiB TrueNAS deployment | PASS | The live Host reports clean revision `2a7383a`; its read-only 1,813,217,565,696-byte export has 32 KiB clusters, a 432,199-sector FAT per copy, and passes independent FAT32 probing |
| Physical USB Loader GX 32 KiB large-library retest | FAIL | USB Loader GX still froze at `Initializing USB devices`; across a 40-second sample the Pi remained ready with NBD requests fixed at 143, zero failures/reconnects/resets, and USB configured |
| Live large Wii FSInfo diagnosis | PASS | Both live FSInfo fields are `0xffffffff`. USB Loader GX r1283 bundles custom libfat 1.1.5, whose `_FAT_partition_readFSinfo` responds to the unknown free-count sentinel by calling `_FAT_fat_freeClusterCount` and walking the complete FAT during `fatMount` |
| Exact large Wii FSInfo generation | PASS | The builder reserves at least one real free cluster, publishes exact nonzero free-cluster and next-free values in primary and backup FSInfo sectors, verifies that the advertised cluster is unallocated, and retains valid FAT32/MBR limits |
| Exact FSInfo replacement OCI | PASS | Full tests, independent FAT32 validation, targeted race tests, static checks, Compose validation, hardened container lifecycle, and OCI inspection pass locally; dirty-worktree candidate digest `sha256:93d6056a9ce0dab0030a55e7a74abf76c7ee64f564c8899b430fbb50cc350ce8` is not a published release |
| Exact FSInfo TrueNAS deployment | PASS | Commit `8e8ec40`; `ghcr.io/OWNER/wiibridge-host@sha256:aa1a5a6db11320c504daed2c8b198b1fb8bc6836c3a5ef90fb5b4c4adc44707f` is live and Ready. The 1,813,217,599,488-byte export reports one free cluster, points to unallocated cluster 55,321,472, and retains 32 KiB FAT32 geometry |
| Physical USB Loader GX exact-FSInfo USB initialization | PASS | USB Loader GX advanced beyond `Initializing USB devices` for the first time on the large catalog |
| Physical USB Loader GX large-catalog resource stage | FAIL | The stale `Loading resources` screen covered a finite catalog traversal: Pi requests rose from 4,251 to 5,518 while throughput remained active, then stopped with Pi CPU below 1% and zero NBD/USB errors, but the loader did not present its GUI. The remaining fault is after WiiBridge transport, USB mounting, and FAT catalog reads |
| Wii SD recovery and loader-isolation staging | PASS | A verified 3,815-entry allocated-file archive plus raw MBR/boot/FAT metadata were retained before mutation. The dirty bit and divergent FAT copies were repaired, a subsequent `fsck.fat -n` is clean, and the previous loader state is retained both on-card and in the host backup. The staged configuration is Wii-only list mode with header/title/banner caches, GameTDB title use, cache CRC walking, and free-space calculation disabled |
| Physical USB Loader GX clean-SD-cache isolation retest | PENDING | Reinsert the safely unmounted Wii SD card and cold boot r1283 against the unchanged complete WiiBridge catalog |
| Exact live large-Wii FAT/WBFS metadata audit | PASS | Mutual-TLS read-only audit confirms matching primary/backup boot and FSInfo sectors, matching FAT copies, exact one-free-cluster FSInfo, a genuinely free next cluster, 987 archive-only WBFS segments, and no DOS read-only/hidden/system attributes |
| Live WBFS split-boundary diagnosis | PASS | The deployed export contains 59 segments of 4,294,963,200 bytes. With 32 KiB clusters each chain spans exactly 2^32 bytes; independent `fsck.fat -n` reports a zero-byte chain, while `mshowfat` confirms the physical chain exists. USB Loader GX r1283 uses 4 GiB minus 32 KiB specifically to remain one cluster below this overflow boundary |
| USB Loader GX split-boundary correction | PASS | Focused tests now emit ordered 4 GiB-minus-32 KiB segments, keep every chain below 2^32 bytes, validate archive-only attributes/LFN checksums/unique aliases, pass sparse independent `fsck.fat`, and preserve exact banner-range and cross-segment source bytes |
| Split-boundary full local validation | PASS | Full tests, targeted Host race, Host vet, static/shell/Pi checks, independent FAT32 test, Compose validation, hardened container test, runtime-identity negative tests, OCI packaging, diff checks, and focused benchmarks pass. The diagnostic OCI remains dirty and is not publishable |
| Split-boundary clean OCI | PENDING | Requires a clean committed revision, matching binary/OCI revision, immutable publication, and independent registry resolution |
| Split-boundary TrueNAS deployment | PENDING | Requires immutable GHCR publication, operator installation, running revision/digest proof, regenerated live snapshot audit, and Pi reconnect |
| Physical catalog/banner/gameplay retest after split correction | PENDING | Requires three cold/warm catalog timings, 3/3 banners, three sustained game tests, and reconnect coverage on the physical Wii |
| TrueNAS container memory bound | PASS | Compose parser accepts a 384 MiB Go target inside a 512 MiB hard limit |
| GameCube 10,000-extent lookup | PASS | Binary search benchmark: 10.51 ns/op, approximately 95.1 million lookups/s, 0 B/op, 0 allocs/op |
| GameCube source-handle bound | PASS | Repeated reads reuse one handle; 40-source test and 10,000-read test never exceed the 32-handle LRU limit; close releases all handles |
| GameCube read coalescing | PASS | A 1 MiB request inside one ISO produces one source ReadAt; a 128 KiB request crossing two source extents produces exactly two |
| GameCube host performance matrix | PASS | Sequential, random, 32-source switching, 5,000-file FST, boundary, and concurrent benchmarks recorded in `reports/gamecube-no-copy-performance.json` |
| End-to-end physical GameCube performance | DEFERRED_HARDWARE_UNAVAILABLE | Host benchmarks and mutual-TLS protocol tests do not prove Pi Zero W/Wii/Nintendont gameplay throughput |
| Bounded Wii Host startup | PASS | 512 GiB fixture with 1,048,577 FAT sectors per copy builds from 131 compact chains in under one millisecond without walking the apparent FAT |
| Responsive UI during source scan | PASS | HTTPS and a phase-specific startup page are available before library walking; NBD remains unavailable until the validated Wii export is complete |
| Diagnostic startup and health logging | PASS | Phase transitions, 30-second heartbeats, scan counts, elapsed time, and exact CA/TLS/HTTP health errors are logged |
| Legacy loopback health certificate | PASS | Local check validates the trusted server-auth chain without requiring a 127.0.0.1 SAN; external identity verification is unchanged |
| HTTPS liveness before persistent/library work | PASS | Startup handler and listener are installed before LibraryManager, browser auth, SQLite, Wii scan, or GameCube scan; delayed-phase and failure regressions pass |
| Separate Host readiness | PASS | `/healthz` remains live during startup while `/readyz` stays 503 until the Wii backend/export manager is safe |
| GameCube startup validation bound | PASS | Fast validation checks compact generation data and source identities without payload hashing; deep hashing is background, cancellable, receipt-backed, and does not block Wii |
| Immutable binary and OCI source identity | PASS | Host `version` reports commit/build/dirty/Go/target and OCI builders emit revision/version/created/source labels with CI equality checks |
| Corrected TrueNAS startup deployment | PENDING | Exact digest, binary revision, OCI label, prompt 8445 liveness, and two consecutive restart timings require the operator's TrueNAS runtime |
| Lifetime Pi certificate Host publication | PASS | Main revision `7fd0c3a`; GitHub Actions run `33214221649`; independently resolved GHCR digest `sha256:4df501302aee625f50f125b5e21c4f3c7402408cd40cdfebaab8d7991227749b` |
| Lifetime Pi certificate TrueNAS deployment | PENDING | Digest-pinned YAML is published; running revision, health, readiness, and Pi-manager startup require operator deployment evidence |

| Separate Wii/GameCube library configuration | PASS | Legacy shared root and independent platform roots; per-platform status, scanning, diagnostics, and runtime failures verified with synthetic fixtures |
| Explicit moved-mount recovery | PASS | Ordinary rescan rejects stale mount; confirmed recovery requires read-only complete non-empty scan, mount recheck, and atomic trusted-identity/catalog/snapshot commit; failure rollback tested |
| GameCube rebuild after source relocation | PASS | Fresh scanned paths remain playable, old generation is blocked, replacement builds successfully, and a configured-root change cannot activate the old generation |
| Separate-library container acceptance | PASS | Real read-only mounts, moved files, stale persisted identity, empty replacement preservation, and GameCube build/activation during Wii source outage pass; source hashes unchanged |
| Separate-library image publication and TrueNAS deployment | PENDING | Local dirty build is tested; requires publication, actual operator dataset paths, updated runtime image, and mount verification |
| Separate-library physical Pi/Wii/Nintendont acceptance | DEFERRED_HARDWARE_UNAVAILABLE | Container and synthetic tests do not establish physical gameplay/save behavior |
# Full performance audit gate — in progress

- Baseline main: `cbb1d7842d866a7ba3969a2a8ae39679ccd5cd27`.
- Baseline complete software validation and three-target firmware release: PASS.
- Baseline hardened OCI, libnbd mTLS, Linux NBD and synthetic payload checks: PASS.
- Repeated ext4 benchmark baseline: captured; optimized comparison pending.
- Optimized complete validation, artifact provenance and publication: PENDING.
- Physical Wii/GameCube launches, real TrueNAS storage and Pi USB timing:
  `DEFERRED_HARDWARE_UNAVAILABLE`.
