# Worklog

## 2026-07-24 — Baseline

- Read the complete 1,678-line controlling prompt.
- Found an initial repository commit containing only project metadata; those
  tracked files were deleted in the incoming worktree and are being recreated
  to implement the requested system.
- Build host: Debian 13.6 amd64, kernel 6.12.96, approximately 167 GiB free.
- Available initially: Go 1.24, GCC 14, QEMU user emulators, bmaptool, OpenSSL,
  dosfstools, systemd validation, shellcheck, and xz.
- Docker/Podman, nbdkit command-line tools, and a connected TrueNAS target were
  not detected during the initial sandboxed inventory.
- Exact TrueNAS version, Apps backend, target datasets, ACLs, target CPU, and
  target port availability remain `TARGET_NOT_CONNECTED`; no deployment result
  is claimed.
- All physical tests are `DEFERRED_HARDWARE_UNAVAILABLE`.

## 2026-07-24 — Server, bridge, and release build

- Implemented scanner, immutable FAT32/MBR synthesis, direct source extents,
  source-mutation failure, SQLite snapshots, HTTPS control plane, and a bounded
  fixed-newstyle read-only NBD server.
- Passed Go unit/integration tests, 1,000-entry catalog synthesis, independent
  FAT32 fsck, real libnbd mutual-X.509 interoperability, and a Linux kernel NBD
  read-only mount. NBD uses the documented TLS 1.2 compatibility profile;
  HTTPS uses TLS 1.3.
- Built and tested the hardened non-root amd64 container with a read-only root,
  no capabilities, no privileged mode, no Docker socket/devices, persistent
  metadata, and an unchanged read-only WBFS source.
- Produced and independently parsed the TrueNAS Compose artifacts. No TrueNAS
  target is connected, so live TrueNAS facts and deployment remain external.
- Implemented Pi controller, bounded cache, board rejection, first-boot
  identity, fixed-action privileged helper, ConfigFS gadget lifecycle, systemd
  hardening, three pi-gen profiles, packaging, and offline validation.
- Discarded initial parallel pi-gen attempts after shared chroot `/dev/pts`
  teardown collisions. Later rejected attempts exposed missing stage-copy and
  pi-gen hook/export integration; no image from those attempts was accepted.
- Completed clean sequential pi-gen builds for Pi 5 ARM64, Pi 4 ARM64, and
  Zero W ARMHF. Sanitized machine identity and SSH host keys after pi-gen export,
  then validated the exact release images.
- All three images passed partition inspection, non-destructive FAT/ext4 checks,
  read-only inspection, architecture/board metadata, required boot and gadget
  configuration, systemd/application presence, QEMU application smoke tests,
  and embedded-payload/identity scans. Pi 4 and Pi 5 hashes are distinct.
- Generated each board's uncompressed/compressed checksums, bmap, package
  manifest, SPDX SBOM, provenance, offline report, and retained build log.
- Rebuilt the final amd64 OCI layout at
  `sha256:14dfeb29b5ac206c479be45a32bb1752ab56951c099053e757ad8f2915b50461`.
  Reran the hardened container, libnbd mutual-TLS, Linux kernel NBD read-only
  mount, Go, static, systemd, and Compose validation gates successfully.
- Final local status is `SOFTWARE-COMPLETE RELEASE CANDIDATE — HARDWARE
  UNVERIFIED`. Live TrueNAS deployment is still `BLOCKED_EXTERNAL` because no
  target is connected; full-build byte reproducibility remains `PENDING`.

## 2026-07-24 — GitHub publisher diagnostics

- Diagnosed a failed publication as an invalid active GitHub CLI token for
  the configured account; no upload was claimed.
- Updated `scripts/publish_github.py` to retain command stderr in failures and
  derive an explicit `OWNER/REPO` selector from the configured Git remote.
- Passed Python bytecode compilation and verified that the current `origin`
  resolves to the configured GitHub repository. A live upload was not attempted because
  GitHub CLI authentication must first be renewed.

## 2026-07-24 — GitHub Docker workflow

- Replaced two duplicate GitHub Docker workflow templates with one bounded
  workflow using `ubuntu-latest`, read-only repository permissions, concurrency
  cancellation, and a 20-minute timeout.
- Corrected the build to install the Go version declared by `go.mod`, create
  the required static amd64 server binary, and use
  `server/packaging/container/Dockerfile`.
- Parsed the workflow YAML, passed `git diff --check`, built the exact static
  server input, and successfully built the resulting local Docker image
  `wiibridge-host:workflow-test`.
- The first corrected GitHub-hosted run passed all steps in 54 seconds. Updated
  Checkout and Setup Go to their Node.js 24 action releases after that run
  reported the older Node.js 20 action runtime as deprecated.

## 2026-07-24 — GHCR publishing and HTTPS port migration

- Added authenticated GHCR publishing for main-branch builds using the
  repository-scoped `GITHUB_TOKEN` and `packages: write`; no credential is
  recorded in the workflow.
- Migrated the server's internal HTTPS listener, container exposure, TrueNAS
  mapping, health check, examples, and operational documentation from TCP 8443
  to TCP 8445 because 8443 conflicts with an existing service.
- Passed server tests, static Compose policy validation, Docker Compose parsing,
  YAML parsing, formatting, and whitespace validation before publication.
## 2026-07-25 — First physical Zero W boot and setup-path repair

- The first physical Raspberry Pi Zero W boot of `0.1.0-rc.1` exposed a failed
  `wiibridge-recover.service` and no configuration access point.
- Read-only card inspection proved first-boot identity generation completed,
  while the recovery helper rejected `detach` before provisioning, the image
  contained no AP profile/service or provisioning implementation, and the
  controller could not traverse its credential directory.
- Reworked cleanup actions so detach/disconnect remain fail-safe without bridge
  configuration; added a device-unique WPA2 NetworkManager setup/recovery AP,
  forced-recovery boot marker, authenticated and CSRF-protected HTTPS
  provisioning form, typed root provisioning helper, login rate limiting,
  corrected service permissions, and bounded persistent journals.
- Added controller tests and expanded shell, systemd, secret, and firmware
  static validation. `make test` and `make static` pass. The rebuilt static
  ARMv6 controller starts under QEMU.
- Repaired the connected card after an ext4 journal recovery. Device identity,
  admin token, and TLS credential hashes matched before and after. The card
  passes systemd verification, target-user controller startup, secure AP
  profile inspection, and unprovisioned recovery-detach execution.
- Physical reboot, AP association, HTTPS setup, Wi-Fi transition, NBD/TLS, USB
  gadget, Wii, and USB Loader GX tests have not yet been run on the repaired
  card. The prior release candidate is withdrawn pending retest and rebuild.
- The first repaired-card reboot confirmed `wiibridge-recover.service`
  finished successfully and the controller started as intended. The setup AP
  still failed: persistent logs showed NetworkManager/wpa_supplicant attempting
  AP mode, followed by BCM43430 `key setting validation failed` and a
  supplicant timeout.
- Replaced NetworkManager AP mode with dedicated `hostapd` and `dnsmasq`
  services while retaining NetworkManager for client Wi-Fi. Installed the
  repository-matched Raspbian ARMHF hostapd package after verifying its SHA-256,
  masked the generic package service, preserved the device-specific AP
  passphrase, and installed only the hardened WiiBridge service graph.
  Offline hostapd parsing, dnsmasq syntax, systemd verification, package
  architecture, and device identity preservation pass. Physical association
  with the new AP remains pending.
- Physical hostapd retest then confirmed `AP-ENABLED`, iPhone association,
  `WPA: pairwise key handshake completed (RSN)`, and
  `EAPOL-4WAY-HS-COMPLETED`. The apparent password wait was DHCP: the hardened
  dnsmasq service repeatedly failed because its default lease path was
  read-only.
- Moved the lease database to
  `/run/wiibridge-dnsmasq/dnsmasq.leases`, added a systemd-owned runtime
  directory, and ran dnsmasq as its unprivileged account with only
  `CAP_NET_ADMIN`, `CAP_NET_BIND_SERVICE`, and `CAP_NET_RAW`. Full automated
  tests, dnsmasq syntax validation, systemd verification, card identity
  preservation, and filesystem checks pass. Physical DHCP and HTTPS setup
  access remain pending.
- The DHCP-runtime retest associated successfully but assigned the client a
  link-local address. Persistent logs identified the remaining
  failure precisely: the unprivileged dnsmasq process could not traverse the
  protected `/etc/wiibridge` credential directory to read its configuration.
- Moved the non-secret DHCP configuration to the root-owned, world-readable
  `/etc/wiibridge-dnsmasq.conf` path while keeping all credentials protected.
  Repaired the live card without changing the AP passphrase, admin token,
  device certificate/key, machine identity, or setup file. `make test`,
  `make static`, systemd verification, and dnsmasq configuration parsing under
  the card's actual dnsmasq UID/GID pass. Physical DHCP and HTTPS setup access
  require one more reboot test.
- The next physical test obtained DHCP successfully and loaded the HTTPS login
  page, proving the hostapd, dnsmasq, routing, controller, and TLS setup path.
  Manual authentication with the original 64-character management credential
  remained unsuccessful.
- Changed new-device management passwords to 12 lowercase hexadecimal
  characters and changed the controller minimum accordingly. The live-card
  repair requires an explicit password-reset option, so routine repairs retain
  credentials. Added boundary tests, rebuilt the static ARMv6 controller, and
  reset only the live card's management password. The new value matches
  `WIIBRIDGE-SETUP.txt`; AP passphrase, TLS certificate/key, and machine
  identity hashes remain unchanged. Automated and offline checks pass; physical
  login with the short password remains pending.
- After the test card was reported corrupted, rebuilt the complete Zero W
  image from the current working tree rather than restoring the obsolete
  pre-repair artifact. The new build includes the ARMv6 controller, hostapd,
  dnsmasq runtime/configuration repairs, first-boot/recovery changes, and
  12-character management-password policy.
- The rebuilt image passed FAT/ext4 inspection, board and boot metadata,
  service/package presence, secret/identity absence, ARM architecture, QEMU
  smoke, checksums, compressed-image expansion, SBOM, and provenance checks.
  Flashed it to the confirmed removable device (local path redacted).
- A whole image-region hash differed because bmap intentionally leaves
  unallocated sparse ranges untouched on reused media. Direct post-write
  comparison of all 163 allocated bmap ranges passed. The flashed card then
  passed FAT/ext4 checks, clean first-boot identity state, enabled-service
  inspection, exact controller-binary comparison, systemd verification, and
  dnsmasq parsing as its actual unprivileged UID/GID. Fresh first boot and
  management login remain pending.
- The fresh image completed its first physical Zero W boot and expanded the
  root filesystem. Persistent logs prove first-boot identity generation and
  recovery completed, the setup AP, hostapd, dnsmasq, and controller started,
  hostapd reached `AP-ENABLED`, and dnsmasq opened its intended DHCP range on
  `wlan0`. No WiiBridge failure appears in the boot journal.
- The abrupt card removal after that boot left only normal FAT/ext4 dirty
  state. Offline repair cleared the FAT dirty bit and recovered the ext4
  journal; repeat non-destructive filesystem checks are clean. The generated
  setup file matches the device SSID, setup Wi-Fi credential, 12-character
  management credential, and management URL. Client login remains pending.
- The fresh-image management login passed: the authenticated network-setup
  form accepted the 12-character management credential and reached submission.
  Client Wi-Fi provisioning then failed before profile creation because the
  controller unit's `ProtectSystem=strict` sandbox exposed only
  `/run/wiibridge` as writable to the root provisioning helper.
- Added only the three persistent provisioning targets to the controller
  unit's `ReadWritePaths`: the NetworkManager connection directory,
  `/etc/wiibridge`, and `/boot/firmware`. Filesystem ownership still blocks
  direct writes by the unprivileged controller. `make static`, `make test`,
  systemd verification, and whitespace checks pass.
- Installed the corrected unit on the physical card without resetting any
  credential or identity. No partial client profile or provisioned marker was
  present after the failed submission, and the setup file, management
  credential, AP credential, certificates, hostapd configuration, and machine
  identity were preserved. Client provisioning must now be retried.
- The repaired physical provisioning retest passed. The Pi saved the submitted
  2.4 GHz client profile and joined the configured home network after reboot.
  Local management now moves to HTTPS port 9443 on the Pi's DHCP address; NBD
  server/TLS provisioning and attachment remain pending.
- Revised provisioning so an existing client Wi-Fi profile is retained when
  all three Wi-Fi fields are blank; partial Wi-Fi updates remain rejected.
  Existing bridge settings likewise remain stored when their fields are blank.
- Added CSRF-protected browser buttons for server test/connect/disconnect and
  USB attach/detach, plus dashboard reporting for the automatically discovered
  USB Device Controller and its live link state.
- Added an opt-in boot service that tests mutual-TLS NBD, connects read-only,
  and attaches the USB gadget only when a complete bridge, an authorized
  VID/PID pair, and the auto-attach marker are present. Forced recovery disables
  it, failures clean up the connection, and systemd bounds retry attempts.
- Full Go tests, shell analysis, systemd verification, static checks, an ARMv6
  controller rebuild, and QEMU smoke pass. The feature is not yet installed on
  or physically tested with the live card, and no USB identity was invented.
- Installed the rebuilt ARMv6 controller, retained-WiFi provisioning helper,
  typed USB/NBD helper, and opt-in auto-attach unit on the physical Zero W
  card. The installed files exactly match the validated source and the complete
  systemd graph verifies against the mounted card.
- The card's client Wi-Fi profile, bridge material if present, setup file,
  management/AP credentials, TLS identity, hostapd configuration, and machine
  identity were byte-for-byte preserved. The auto-attach marker remains absent,
  so the new service cannot attach unexpectedly. Physical boot, UI, server,
  Linux USB-host, and Wii tests remain pending.

## 2026-07-25 — TrueNAS TLS health-check repair

- Confirmed the TrueNAS target at its private address is reachable and TCP ports
  8445 and 10809 accept connections. The deployed HTTPS health endpoint returns
  success, but strict certificate inspection proves that listener is still
  serving an older certificate that does not chain to the replacement CA.
- Identified the repeat startup defect: the Compose health check connects to
  `127.0.0.1`, while the newly issued server certificate originally contained
  only the external private IP subject alternative name.
- Updated `scripts/tls-provision.sh` to issue server certificates for the
  requested DNS/IP identity plus loopback `127.0.0.1`, with bounded IPv4
  validation. Hostname, IPv4, loopback-only, and invalid-IPv4 generation checks
  pass.
- Reissued only `server.crt` and `server.key` under the existing CA, preserving
  the CA and Pi client identity. The prior server pair is retained in a private
  rollback directory.
- Verified the replacement certificate chain, both IP identities, key match,
  and validity. Started the packaged server locally with the replacement bundle
  and passed its exact built-in health-check command against the loopback URL.
- TrueNAS SSH is refused and no authenticated management API/shell is
  connected. The replacement files are ready, but target-side installation and
  an application restart remain externally blocked.

## 2026-07-25 — Pi Zero NBD module preload repair

- The first browser server-connect action reached the privileged helper but
  failed with `modprobe: FATAL: Module nbd not found in directory
  /lib/modules/6.18.34+rpt-rpi-v6`.
- Verified the exact Zero W image contains the matching
  `kernel/drivers/block/nbd.ko.xz`, its dependency index entry, and
  `CONFIG_BLK_DEV_NBD=m`. This ruled out an omitted module or kernel/modules
  version mismatch.
- Reproduced the relevant systemd behavior: `ProtectKernelModules=yes` hides
  the module tree even from a root helper inherited through the protected
  controller service, so `modprobe` reports an existing module as not found.
- Added an early-boot `systemd-modules-load` entry and bounded
  `nbds_max=1` option. Removed module loading from the protected helper; it now
  requires the preloaded module and `/dev/nbd0`. Kernel-module protection
  remains enabled.
- Added explicit service ordering and strengthened offline image validation to
  require the NBD preload configuration, dependency entry, and module file for
  every included kernel. Unit, static, shell, systemd, and whitespace checks
  pass.
- The reconnected card had been removed without a clean shutdown. Cleared the
  FAT dirty state and recovered the ext4 journal/free-block accounting, then
  confirmed both filesystems were clean before installing the repair.
- Verified the card contains the exact matching v6 NBD module and dependency
  entry, installed the boot preload/options, corrected helper and service
  ordering, and verified the complete systemd graph.
- Hash comparisons confirmed the client Wi-Fi profile, bridge configuration,
  CA/client TLS material, device certificate/key, management credential,
  setup file, hostapd configuration, and machine identity were preserved.
  Cleanly unmounted the card and passed final non-destructive FAT/ext4 checks.
  The physical reboot then passed NBD preload and reached TLS negotiation.

## 2026-07-25 — Pi client TLS file diagnosis

- The physical connect action advanced beyond the repaired NBD module stage
  but `nbd-client` reported that the installed client certificate/key could not
  be loaded by GnuTLS.
- Verified the generated Pi pair has a valid client-auth certificate, matching
  private key, valid chain and dates, and parses successfully with OpenSSL and
  GnuTLS.
- Strictly verified the replacement TrueNAS HTTPS certificate is now live.
  Connected to the live NBD export with the generated Pi identity; mutual TLS
  passed, the export is read-only, and its virtual size is 1,284,936,704 bytes.
  This isolates the remaining failure to the certificate/key copy installed on
  the Pi.
- Added provisioning-time client-purpose chain verification so a future
  mismatched or unrelated client certificate cannot be saved alongside the
  server CA. Full unit, static, shell, systemd, and whitespace checks pass.
- Offline card inspection identified the immediate cause: `ca.crt`,
  `client.crt`, and `client.key` were all zero bytes after another unclean
  removal. Recovered the FAT/ext4 filesystems, then installed exact copies of
  the live-verified bundle with the intended root/wiibridge ownership and
  mode 0640.
- Added an authenticated, CSRF-protected, typed safe-poweroff dashboard action.
  It fail-safely detaches USB and NBD, flushes storage, and uses an exact sudo
  rule to request systemd poweroff without blocking the web response.
- Rebuilt the static ARMv6 controller and passed unit, static, shell, systemd,
  QEMU smoke, and whitespace checks. Installed the controller, helper,
  provisioner, sudo rule, and TLS bundle on the physical card.
- Verified the card certificate chain, client purpose, key match, OpenSSL and
  GnuTLS parsing, exact bundle hashes, and service graph. Hash comparisons
  preserved Wi-Fi, bridge configuration, device identity, management
  credential, and setup/AP state. Final FAT/ext4 checks are clean; physical
  connection and safe-poweroff retests remain.
- The repaired physical retest loaded the client identity, completed mutual
  TLS negotiation, and received the expected 1,225 MB export size. This closes
  the client-file failure.
- `nbd-client` then failed its generic-netlink `NBD_CMD_CONNECT` while handing
  the negotiated socket to the kernel. Source inspection confirms the displayed
  error is emitted only when the netlink setup request is rejected. Persistent
  kernel logs are required to distinguish an occupied device, capability
  restriction, or Pi-kernel netlink incompatibility before applying another
  repair.

## 2026-07-25 — NBD idle timeout and USB VID/PID propagation repair

- Used the new dashboard poweroff action on the physical Pi and inspected its
  persistent journal after shutdown. Both card filesystems were clean, which
  physically validates the safe-poweroff path.
- Corrected the earlier NBD diagnosis: the first kernel setup succeeded.
  Journal and kernel evidence show `Connected /dev/nbd0`, the expected
  1,284,936,704-byte capacity, and partition `p1`.
- Correlated the Pi journal with the TrueNAS log. The deployed server closed a
  healthy idle NBD session after about 33 seconds because the 30-second
  negotiation deadline was retained during transmission. Subsequent attempts
  found `nbd0` already in use and emitted the misleading generic setup error.
- Changed the NBD server to clear the connection deadline once negotiation
  enters transmission. Added a regression test that remains idle beyond the
  negotiation deadline and then successfully performs a read.
- Hardened the Pi helper to wait for udev and read sector zero during its test
  action, and to reject an active connection while cleaning up stale NBD state
  before a real reconnect.
- Offline card inspection showed that a syntactically valid authorized USB
  VID/PID pair and the auto-attach marker were already present. The gadget
  failure was caused by the helper not exporting the sourced values to its
  child process. Added the missing exports without recording the pair's values.
- Passed `make test`, `make static`, `make server`, the hardened container
  lifecycle test, and whitespace validation. Built the replacement TrueNAS
  image archive
  `wiibridge-host-0.1.0-rc.1-idlefix.1.docker.tar` with its checksum and
  target installation guide.
- Installed the updated helper and auto-attach unit on the physical card.
  Exact checks confirmed that Wi-Fi, TLS, bridge configuration, authorized
  pair, auto-attach choice, credentials, and machine identity were preserved.
  Final non-destructive FAT and ext4 checks passed.
- Live TrueNAS deployment remains externally blocked because no authenticated
  management shell or API is connected. The Pi must not be booted against the
  old server image for the final USB retest.

## 2026-07-25 — GHCR publication and live idlefix verification

- Published the repaired amd64 container as
  `ghcr.io/OWNER/wiibridge-host:0.1.0-rc.1-idlefix.1` from source commit
  `1fd587a2cf4e106575e8f13ddc2ab2ed34389fda`. Verified the remote manifest
  digest and anonymous pull access.
- The operator deployed the new image on TrueNAS. Strict HTTPS validation with
  the replacement CA passed against the private TrueNAS endpoint.
- Connected to the live export on port 10809 with the known-good client
  identity through a read-only libnbd FUSE client. The exported size was
  exactly 1,284,936,704 bytes.
- Held the live NBD transmission idle for 35 seconds, beyond the former
  30-second deadline, and then successfully read sector zero. This confirms
  the deployed server no longer drops a healthy idle block-device session.
- The remaining validation is the controlled physical Zero W boot, an
  additional idle interval through its kernel NBD client, and Windows USB
  gadget enumeration/read.

## 2026-07-25 — Windows FAT32 geometry diagnosis and repair

- After rebooting to clear prior stale NBD state, the physical Zero W remained
  NBD-connected beyond the former failure interval while its HTTPS controller
  stayed reachable throughout a monitored 90-second period.
- Windows enumerated the USB mass-storage gadget. The dashboard reported USB
  attached and link state `configured`, closing the VID/PID propagation and
  gadget-enumeration failures.
- Windows requested formatting rather than mounting the read-only export.
  A separate read-only connection to the same live export confirmed a valid
  DOS partition table with one type-0x0c partition at sector 2,048, then
  reproduced the rejection with `mtools`: the FAT32 BPB advertised zero heads
  and zero sectors-per-track.
- Updated the synthesized FAT32 BPB to record conventional LBA-assisted
  geometry of 63 sectors/track and 255 heads, plus the partition start as
  2,048 hidden sectors.
- Added exact unit assertions and an independent `mtools` directory-read
  regression alongside the existing `fsck.vfat` test. Full unit, static, and
  whitespace validation passes.
- The hardened container lifecycle passed with the repaired builder. Published
  the cumulative replacement as
  `ghcr.io/OWNER/wiibridge-host:0.1.0-rc.1-idlefix.2`, verified its
  remote manifest digest and anonymous pull access.
- After target redeployment, strict HTTPS and mutual-TLS NBD connection passed.
  The live export reports the corrected 63 sectors/track, 255 heads, and 2,048
  hidden sectors. Independent read-only `mtools` access to both the filesystem
  root and `/wbfs` directory passes. Physical Windows remount is the remaining
  step.

## 2026-07-25 — Windows fixed-disk identity and Pi readiness repair

- The idlefix.2 Windows remount still requested formatting. A live read-only
  loop-device probe identified the export as FAT32, `fsck.fat` passed, the
  Linux kernel mounted it read-only, and `/wbfs` was readable.
- Inspected the safely shut-down Pi's persistent journal from the exact
  attempt. The final NBD connection remained active until the requested
  poweroff, USB reached high-speed configured state, and no host-read, gadget,
  or NBD errors occurred while Windows inspected the disk.
- The journal also showed the first auto-attach test unnecessarily failing
  because global `udevadm settle --timeout=5` timed out, then disconnecting
  while partition reads were pending. A bounded retry later succeeded.
- Isolated the remaining Windows-specific metadata defect: the gadget presents
  a fixed, read-only MBR disk, while the MBR signature was zero. Added a
  deterministic nonzero per-catalog MBR signature and matching FAT volume ID
  so Windows receives a stable unique disk/volume identity without needing to
  write one.
- Replaced global udev settling with a bounded wait for `/dev/nbd0p1` and
  direct reads from both the whole disk and partition before disconnecting the
  test session.
- Full unit, FAT, static, shell, and whitespace validation passes. Installed
  the updated helper on the physical card; Wi-Fi, TLS, bridge configuration,
  authorized VID/PID, auto-attach state, credentials, and machine identity
  were preserved, and both card filesystems remain clean.
- The hardened container lifecycle passed. Published cumulative idlefix.3 from
  commit `c2a529479fb3d93d46e25e6da157dea1e001994f`, verified the remote
  manifest digest and anonymous pull access. TrueNAS deployment and physical
  Windows retest remain pending.
- After target deployment, strict HTTPS and mutual-TLS NBD passed. The live
  export has a nonzero deterministic MBR signature, a matching FAT volume ID,
  the corrected geometry, and passes independent `mtools`, `fsck.fat`, and
  Linux kernel read-only mount checks. Physical Windows remount is next.

## 2026-07-25 — Windows recognition and first Wii attempt

- The operator reported that Windows now detects and visually presents the
  idlefix.3 partition. This closes the fixed-disk identity defect at the
  partition-recognition layer; a separate explicit File Explorer `/wbfs` read
  has not yet been recorded.
- On the first available Wii test, USB Loader GX crashed during startup when
  the Zero W bridge was connected. No game launch was attempted, and no exact
  loader revision, cIOS inventory, crash screen, or Pi journal from the
  attempt has been captured yet.
- Inspection of the current official USB Loader GX r1283 source confirms that
  USB port 0 is the default and that USB initialization occurs early in its
  startup. Its release notes include partition-detection and size-calculation
  repairs, and its official setup requires current d2x cIOS.
- The next controlled test is to leave the Wii at its system menu for 90
  seconds so the Pi can finish booting, connect NBD, and attach the gadget
  before launching the loader. Loader revision, crash mode, selected USB port,
  and whether the app/configuration reside on SD must be captured before any
  speculative disk-format or gadget change.
- The follow-up result advances beyond startup: USB Loader GX mounts the
  virtual storage and displays the game file, but crashes when asked to boot
  it. A post-failure controller probe still reports NBD connected, USB
  attached, and gadget state `configured`. The filesystem/catalog path is
  therefore physically proven through the Wii, while cIOS handoff, transient
  USB reset behavior, and full WBFS payload validity remain under diagnosis.
- Persistent journal inspection from two launch attempts shows the Wii
  resetting and re-enumerating the DWC2 gadget at the cIOS handoff. Both
  sequences reached high speed and cleared the bulk endpoint halts, while DWC2
  warned that it could not clear a halt on control endpoint zero. NBD remained
  connected and no block-read, TLS, or network error occurred during either
  attempt. The operator reports a clean reboot to the Wii System Menu rather
  than a DSI exception.
- Mounted the live export read-only and confirmed that it contains one WBFS
  file in a single contiguous FAT cluster extent. Wiimms ISO Tool 3.01a
  verified both the update and data partitions as `OK`; source payload
  corruption and FAT fragmentation are not supported by the evidence.
- Added the Linux mass-storage function's supported no-stall setting before
  binding the gadget. This preserves the read-only LUN and immutable NBD
  backing while avoiding bulk endpoint stalls during the legacy d2x reset
  sequence. Static, shell, and whitespace validation pass, and the exact
  helper is installed on the physical card for the next Wii boot test.
- The physical no-stall retest still returned cleanly to the Wii System Menu
  when game boot was requested. This rejects endpoint-stall suppression as the
  repair. Removed the override and its temporary validator assertions from
  source, and restored the normal gadget helper on the card.
- Installed and enabled temporary, narrowly filtered boot-time tracing for the
  mass-storage SCSI path, DWC2 halt handling, NBD request lifecycle, and block
  errors. Trace output persists in
  `/var/log/wiibridge-usb-trace.log`; Wi-Fi, TLS, bridge configuration,
  automatic attach selection, and machine identity remain unchanged.
- Verified exact installed files, service enablement, retained state, shell
  syntax, systemd unit validity, the complete unit/static suite, and whitespace.
  Non-destructive FAT and ext4 checks both pass, and the reader was powered off
  before removal.
- The next physical launch reproduced the clean return to the Wii System Menu.
  The normal journal again shows the Wii resetting and re-enumerating the
  gadget, with the live NBD session intact and no network, block-read, or TLS
  failure. The intended trace file was zero bytes even though its service
  stayed active and consumed 78 seconds of CPU.
- Isolated the instrumentation defect to reopening the expensive tracefs
  function inventory for each requested symbol on the ARMv6 Pi Zero. Reworked
  initialization to scan that inventory once, write progress directly to the
  protected capture file and persistent journal, reduce the trace buffer, and
  signal systemd readiness only after tracing is active. USB auto-attach now
  waits for that readiness signal. Installed the corrected helper/unit exactly
  with retained Wi-Fi, TLS, bridge, auto-attach, and machine state verified.
  FAT and ext4 checks pass after installation, and the reader was powered off
  before removal.
- The corrected physical capture succeeded with 1,749 trace lines. Every NBD
  request in the real gadget session completed, all five block-error events
  belong to the intentional pre-attach test disconnect, and the Wii/cIOS path
  continued issuing successful reads after USB reset and re-enumeration.
- Captured five write commands from the host. Each returned immediately
  without entering the mass-storage function's data-receive path, matching the
  kernel's explicit `ro` branch that reports `WRITE PROTECTED`. The last
  rejection at monotonic time 209.666114 was followed by gadget disable/reset
  at 210.262302, about 596 ms later. Because earlier rejected writes were
  survived, this is treated as a controlled compatibility hypothesis rather
  than a proven root cause.
- Added a volatile write-compatibility view using a 32 MiB, tmpfs-backed,
  nonpersistent device-mapper snapshot. The NBD origin must remain
  kernel-enforced read-only; host writes are redirected to RAM and disappear
  at detach or reboot. A local loop-device test accepted and read back overlay
  writes while the read-only origin remained byte-identical.
- Added exact overlay setup/cleanup, stale-loop recovery, failure cleanup,
  device size/read-only assertions, device-mapper module preload, explicit
  `dmsetup` packaging, image installation, repair-card installation, and
  strengthened static/offline checks. Full unit/static/whitespace validation
  passes. The physical card has exact updated helper, gadget, overlay, trace
  unit, and module-preload files; retained state is verified. The successful
  strict-read-only capture was preserved separately for comparison. FAT and
  ext4 checks pass after installation, and the reader was powered off before
  removal.
- The physical comparison capture contains 3,560 lines. The overlay accepted
  all five writes and entered the normal data-receive path, 390 reads
  completed, and all 117 NBD requests had matching header and payload replies.
  The only five block errors belong to the intentional pre-attach disconnect
  test. The final accepted write preceded the gadget reset by about 550 ms,
  but USB Loader GX still returned to the System Menu. This rejects SCSI-level
  write protection alone as the cause; the independent DOS read-only file
  attributes remained unchanged. The overlay, device-mapper preload,
  packaging, and validator changes were removed; the strict read-only gadget
  was restored while both traces were retained.
- A new read-only live-export audit found that the server catalog had changed
  from the previously verified 1,284,936,704-byte single-file image to a
  3,619,423,232-byte four-file image with no FAT free space. All four WBFS
  files were copied through mutual-TLS NBD and independently checked with
  Wiimms ISO Tool 3.01a. Three pass; one reports an H0 integrity failure in its
  data partition. The next step is to identify whether that damaged payload
  was the attempted game. If not, a Wii `sysCheck.csv` is required to capture
  the exact base IOS assigned to installed d2x slots 248 through 251.
- The operator identified the failed launch as **10 Minute Solution**. Its
  catalog entry is one of the three WBFS files that passed full verification,
  not the file with the H0 integrity error. Payload corruption is therefore
  eliminated for this launch. The remaining evidence needed is a Wii
  `sysCheck.csv`, because the reported current d2x revision does not reveal
  which IOS base is installed in each of slots 248 through 251.
- The Wii SD card's SysCheck HDE report confirms the expected d2x-v11-beta3
  inventory: slots 248[38], 249[56], 250[57], and 251[58]. The cIOS
  installation is therefore current and correctly based.
- USB Loader GX r1283 was launching this game through its inherited global
  slot 249/base 56 configuration. Applied one reversible per-game comparison:
  the game's `GXGameSettings.cfg` entry now explicitly selects slot 250/base
  57. Alternate-DOL selection resolves to off for this title, other settings
  are unchanged, and no system IOS was installed or modified.
- The Wii SD card's two redundant FAT copies initially differed while each
  remained structurally intact. Reconciled them with `fsck.vfat`, then a
  non-mutating verification passed cleanly. A read-only remount confirmed both
  the SysCheck report and the explicit IOS250 game setting before the reader
  was unmounted and powered off.
- The controlled IOS250/base57 launch returned to the Wii System Menu just as
  the original IOS249/base56 launch did. This rules out the two standard d2x
  game bases as the differentiator. The title has no entry in USB Loader GX's
  built-in alternate-DOL table, so its default setting resolves to off. The
  remaining installed USB-base comparison is IOS251/base58.
- IOS251/base58 also returned to the System Menu. The operator noted a more
  discriminating symptom: this title's normal animated selection banner stays
  blank, and launch gives a brief green flash before returning.
- Reconnected to the current live export read-only through mutual TLS and
  copied the exact attempted WBFS. Both partitions pass WIT v3.05a
  verification and the FAT file occupies one contiguous cluster range.
  Extraction produced a valid 221,872-byte `opening.bnr` with IMET and U8
  signatures and banner/icon/sound members, a structurally valid
  7,101,888-byte `main.dol`, and a TMD that requests IOS53. The source image
  does contain the banner and boot material. A second verified title's banner
  is the next control for distinguishing SD banner-cache state from cIOS
  access to game-internal data through the gadget.
- American Mensa Academy, the second verified title, also displayed a blank
  banner and returned to the System Menu. The brief transition resembles a
  tiled/corrupted Wii-logo framebuffer rather than a game-generated error
  dialog.
- Read-only FAT allocation inspection found 4 KiB clusters and one contiguous
  extent per file. The two verified failures occupy approximately
  1.72–2.92 GiB and 2.92–3.37 GiB of the virtual disk. The verified Squeakquel
  file occupies approximately 0.93–1.72 GiB and is the only clean current
  payload entirely below 2 GiB, making its banner/launch the precise low-LBA
  control. The damaged similarly named file at the start of the disk is
  excluded from testing.
- The low-LBA Squeakquel control also displayed a blank banner and returned to
  the System Menu, eliminating a 2 GiB or high-LBA failure.
- Inspected USB Loader GX r1283's exact WBFS paths. Header enumeration can use
  read-only file access, but banner extraction and game boot call
  `split_open`, whose non-creation path still requests `O_RDWR`. Independent
  `mattrib` inspection of the live export confirmed that all four synthesized
  WBFS entries carry DOS read-only attribute `0x01`. This precisely explains
  why the loader can list titles but cannot reopen them for banner or boot
  access. The earlier writable block overlay retained these file attributes,
  so its failure did not test this condition.
- Changed only the synthesized WBFS file attribute from read-only `0x01` to
  archive `0x20`. The source library, NBD protocol export, and Pi USB LUN
  remain immutable/read-only. Added direct FAT-directory regression coverage
  and an independent `mattrib` assertion. Targeted tests, the complete
  unit/static suite, FAT verification, whitespace checks, and the hardened
  container lifecycle pass. A replacement TrueNAS image must be published and
  deployed before the physical banner and game-launch retest.
- Committed the repair as `74dfb5b` and published the replacement image as
  `ghcr.io/OWNER/wiibridge-host:0.1.0-rc.1-wbfsattrfix.1@sha256:37d94d0c3f11ae8c33f96490fe9c0902b6b417907afd56a14d8dd370f4b2fe80`.
  The registry resolves the digest without stored credentials. TrueNAS
  redeployment and live attribute verification are the next controlled steps.
- After TrueNAS redeployment, strict HTTPS health returned healthy version
  `0.1.0-rc.1`. A fresh mutual-TLS connection reported the expected
  3,619,423,232-byte NBD export and independently confirmed it remains
  read-only. A read-only libnbd FUSE inspection showed archive `A` without
  read-only `R` on all four live WBFS entries, proving the replacement builder
  is active. The Pi controller at its private address was unreachable at that
  point, so a clean Pi reconnect precedes the Wii banner/launch retest.
- The repaired physical path passed. USB Loader GX now displays the 10 Minute
  Solution banner, and the game loads successfully through the Zero W bridge
  and read-only TrueNAS export. Successful enumeration before the repair,
  failed banner/boot access with DOS read-only entries, and immediate success
  after changing only those entries to archive attributes confirm the root
  cause end to end. Sustained gameplay and a later reconnect cycle remain.

## 2026-07-27 — Complete GameCube library no-copy backend

- Confirmed schema 1 summed every disc's physical size, allocated and formatted
  a complete `library.img`, copied ISO/GCM/CISO or every FST file into it, and
  retained multiple payload-bearing generations.
- Replaced the complete-library path with schema 2 compact FAT32 metadata plus
  a sorted source-extent map. NBD reads resolve metadata, immutable source
  ranges, and zero padding without staging complete payloads. A bounded
  read-only handle cache performs identity checks and closes on backend close.
- ISO and GCM map as `game.iso`/`disc2.iso`, CISO maps as
  `game.ciso`/`disc2.ciso`, and extracted FST files map individually to their
  validated original files. FST tree changes invalidate activation.
- Physical memory-card mode is fully read-only. Emulated mode now fails
  configuration explicitly because a bounded save-only overlay is not yet
  implemented; it never falls back to a copied image.
- Schema-1 copied generations are detected and reported but never deleted
  automatically. Offline, explicitly targeted cleanup is documented.
- Measured synthetic fixture: 6,291,456 source bytes, 8,589,934,592 apparent
  virtual bytes, 2,252,800 physically allocated generation bytes, three mapped
  files/extents, and zero overlay bytes. Evidence is recorded in
  `reports/gamecube-no-copy-storage.json`.
- `make test`, `make static`, `make compose`, `make oci`,
  `go test -race ./server/host-daemon/...`,
  `go vet ./server/host-daemon/...`, the many-extent benchmark, and
  `git diff --check` pass. Exact repository-root `gofmt -w .` and
  `go mod tidy` encounter pre-existing unreadable root-owned Pi firmware
  artifacts under `build/pi-gen-zero-w-armhf`; scoped Go formatting passes and
  no module-file change is required.
- No physical GameCube Wii/USB Loader GX/Nintendont test hardware was available:
  `DEFERRED_HARDWARE_UNAVAILABLE`.

## 2026-07-27 — TrueNAS Host memory bound

- Diagnosed the 6+ GiB resident-memory growth as the Wii builder retaining both
  FAT copies in a `map[int64][]byte`, one separate 512-byte allocation per FAT
  sector. The allocation scaled with total virtual library capacity.
- Replaced resident FAT sectors with compact cluster-chain descriptors and
  sector synthesis during reads. MBR, BPB, FAT chains, directory entries,
  payload offsets, read-only behavior, and metadata hashing remain covered by
  the existing FAT32 and integration suites.
- An 8 GiB synthetic fixture uses 16,385 virtual sectors per FAT copy. The old
  representation required at least 16,778,240 raw resident FAT bytes before Go
  map/object overhead; the new representation retains five chain descriptors
  and 21 non-FAT metadata sectors (10,752 bytes).
- TrueNAS Compose now sets `GOMEMLIMIT=384MiB` inside a 512 MiB hard container
  limit. Full tests, static analysis, Compose validation, race tests, OCI
  packaging, and whitespace checks pass. Measurement evidence is in
  `reports/truenas-memory-optimization.json`.
- Completed the mandatory GameCube hot-path acceptance pass. The backend uses
  binary search over 10,000 sorted extents, a strict 32-handle read-only LRU,
  one source `ReadAt` for a 1 MiB request contained in one extent, and closes
  all handles on backend close. A 10,000-read multi-source test measured
  2,320,520 bytes peak heap growth, 32 source opens, 9,968 cache hits, and a
  peak of 32 open files.
- Host benchmark results include 10.51 ns/op and zero allocations for lookup
  among 10,000 extents; 25.6–28.5 microseconds per cached single-source read;
  54.7 microseconds for a request crossing two real source extents; and
  29.4 microseconds under concurrent callers. Full results and limitations are
  recorded in `reports/gamecube-no-copy-performance.json`. No physical hardware
  performance claim is made.

## 2026-07-28 — Bounded Wii startup identity

- Corrected a regression in the on-demand Wii FAT implementation: startup
  still synthesized and hashed every apparent FAT sector when constructing the
  snapshot identity, keeping HTTPS/NBD listeners closed for a long time on
  large virtual disks.
- Snapshot metadata identity now hashes the deterministic compact disk
  geometry, resident metadata sectors, and FAT chain descriptors. It no longer
  walks unallocated virtual FAT capacity.
- A 512 GiB regression fixture with 1,048,577 FAT sectors per copy and 131
  compact chains builds in under one millisecond on the validation Host.
- Sanitized live-looking administrator credentials from the checked-in TrueNAS
  restore example. Previously exposed credentials must still be rotated.
- Moved the HTTPS listener ahead of the two synchronous source-library scans.
  A minimal auto-refreshing page and `/healthz` startup response now expose the
  current scan phase immediately. NBD, Pi coordination, and Wii/GameCube
  exports are not started until the validated Wii backend is complete.
- Added 30-second startup heartbeats, phase durations, scan candidate/game/
  rejection counts, listener-ready logging, and total startup duration.
- Health-check failures now log their CA, TLS, connection, or HTTP error.
  Loopback checks still verify the complete trusted server-auth chain but no
  longer require legacy certificates to contain a `127.0.0.1` SAN.

## 2026-07-28 — Prompt Host liveness and deferred GameCube validation

- Confirmed the remaining 30-minute startup path in source commit `43ccb38`:
  `serve` constructed `NewLibraryManager` before opening HTTPS, and the
  constructor called the deep active-generation validator. That validator
  recalculated every ISO/GCM/CISO hash and every extracted-FST tree hash.
- Confirmed the last operator-provided container identity was commit `4a5ba33`,
  image ID `sha256:1ebeaa...`, registry digest `sha256:1eb7ec...`, with zero
  restarts and no OOM. That image contains neither the bounded-Wii fix nor the
  early-startup-page fix. Registry image `43ccb38` contains both but still has
  the pre-listener manager defect and exposes no binary/OCI revision metadata.
- HTTPS and `/healthz` now open before LibraryManager, authentication, SQLite,
  Wii scanning, or GameCube work. `/readyz` remains 503 until the safe Wii
  backend and export manager are complete. Post-listener startup failures keep
  a bounded diagnostic page available and do not open NBD.
- Split GameCube validation into compact startup checks and cancellable deep
  hashing. A persisted `validation.json` receipt is accepted only while the
  generation checksums and stored source identity set remain current.
  GameCube stays disabled while a missing/stale receipt is regenerated in the
  background; Wii and its NBD export become ready independently.
- Removed synchronous `.building-*` deletion and generation pruning from
  LibraryManager construction. Routine ISO/GCM/CISO and FST catalog scans no
  longer hash full payloads; generation builds and deliberate deep validation
  still do.
- Added structured phase timing, validation file/byte progress, `/readyz`,
  startup failure tests, fast-validation regressions, a startup validation
  benchmark, detailed binary version output, and OCI source identity labels.
- Local fast validation of a three-file, 6,291,456-byte fixture took a median
  7.82 ms and hashed zero payload bytes. The 512 GiB Wii regression with
  1,048,577 FAT sectors per copy built in 672.788 microseconds.
- `make test`, `make static`, `make compose`, `make oci`,
  `go test -race ./server/host-daemon/...`,
  `go vet ./server/host-daemon/...`, focused benchmarks, and
  `git diff --check` pass. Repository-root `gofmt -w .` and `go mod tidy`
  remain blocked by pre-existing unreadable root-owned Pi build artifacts;
  all changed Go files were formatted directly and module files are unchanged.
- TrueNAS redeployment, two-restart timing, and live image/digest/binary
  comparison remain pending because this workspace has no TrueNAS runtime
  access. Evidence and exact acceptance checks are in
  `reports/truenas-startup-investigation.json`.
- GitHub Actions run `30325015690` published clean commit `a6b7c67` as
  `ghcr.io/OWNER/wiibridge-host:sha-a6b7c670c722cecf501d8a89a20e72e7b1a2b15e@sha256:f11c6aed6cd576caeff737100ed4a7254a4dce44960833fece817abe2d78bd11`.
  Pull-back verification reports image ID `sha256:f32a283...`, linux/amd64,
  build time `2026-07-28T03:06:50Z`, `dirty=false`, and equal binary and OCI
  revisions. The TrueNAS paste YAML now pins this digest; live deployment
  remains pending.
- A read-only probe at `2026-07-28T03:10:20Z` reached the live TrueNAS Host.
  `/healthz` returned the old schema with only `status` and `version`;
  `/readyz` redirected to login, while TCP 10809 was reachable. The fixed image
  is therefore not yet running. Runtime-level digest verification still needs
  the TrueNAS shell after the pinned YAML is installed.

## 2026-07-28 — Four-feature implementation inspection and design

- Inspected current commit `ce73242`, schema-2 GameCube generation/read-through
  backend, legacy per-title save code, NBD routing, export switching, Host/Pi
  management channel, scanners, SQLite schema, dashboard, benchmarks,
  diagnostics, deployment, firmware packaging, worklog, and release gates.
- Confirmed complete-library `emulated` mode is rejected in
  `gamecube/library.go`; `fat32virtual.Backend` is wholly read-only; scanners
  have no persistent source-health transaction; the Pi has authenticated
  status/actions but no protocol descriptor; and routine Host metrics contain
  only catalog counts.
- Recorded the scoped implementation map, trusted save-extent and crash
  recovery model, protocol capability matrix approach, source reconciliation
  transaction, bounded metrics/session architecture, security boundary, and
  rollback constraints in `docs/four-feature-implementation-map.md`.

## 2026-07-28 — Save overlay, compatibility, source health, and performance

- Implemented save-overlay format 1 for `emulated-individual` and
  `emulated-shared` GameCube modes. Physical mode remains read-only. Schema-2
  generations now carry host-authored, checksummed writable extents, and the
  NBD backend accepts only complete writes inside one approved card extent.
  ISO/GCM/CISO/CSO/FST source writes and all FAT, directory, padding, and
  unallocated writes remain rejected.
- Added bounded 512-byte dirty-block accounting, a 64 MiB append journal,
  16 MiB pending-write/card/upload limits, same-filesystem checkpoint staging,
  file and directory synchronization, previous-confirmed rollback pairs,
  startup recovery, ambiguity blocking, bounded automatic/manual backups, and
  transactional restore/upload/download/verification. Save administration is
  confined beneath `/data/gamecube/saves`, rejects symlinks and special files,
  and remains independent of payload availability.
- Added descriptor schema 1 and protocol 1 in `shared/compat`. Host and Pi
  descriptors report linked product/revision/build-time/dirty metadata,
  platform/board/device identity, and stable versioned capabilities. Every
  coordinated state-changing action performs a fresh authenticated Pi probe
  under the platform-switch lock and evaluates operation-specific
  capabilities; the persisted compatibility result is display-only.
- Added the additive SQLite schema-2 migration for source roots, catalog item
  state, missing observations, bounded failure events, compatibility display
  state, and bounded performance sessions. Source scans now separate
  read-only preflight, discovery, validation, reconciliation, and commit.
  Failed or partial scans preserve the last complete Wii and GameCube
  catalogs; an absent item requires two complete available scans before
  `missing-confirmed`.
- The migration transaction seeds legacy Wii catalog rows from the active
  schema-1 snapshot. Its historical count also protects the first upgraded
  preflight from an unexpectedly empty failed mount. Runtime source failure
  persistence is normalized to a fixed code set and limited to once per code
  per 30 seconds. Before the first schema-2 transaction, an existing
  checkpointed database is copied and flushed to the non-overwriting
  `wiibridge.sqlite3.pre-schema2.bak` rollback path.
- Active GameCube generations and save associations survive an offline source.
  Reconnect reuses an unchanged trusted generation after identity checks;
  changed sources revoke validation trust. Runtime source-read failures return
  errors rather than zeroes, increment bounded metrics, evict the failed
  handle for controlled recovery, and queue rate-limited reconciliation
  outside the NBD hot path.
- Added fixed atomic counters, fixed latency histograms, 300 one-second rolling
  buckets, bounded warning evaluation, and bounded session summaries across
  source, synthetic disk, NBD/TLS, save, Host runtime/network, Pi runtime/NBD,
  and USB state. The Pi caches system collection for 10 seconds; the Host
  shares one cached Pi sample and never makes telemetry calls from a read
  path. Only bounded session summaries and low-frequency atomic aggregate
  checkpoints are persisted.
- Extended the authenticated, CSRF-protected dashboard with memory-card
  management, compatibility, source-state diagnostics and acknowledgement,
  performance overview/data path/details/session history, and bounded JSON/CSV
  exports. Missing Pi metrics degrades telemetry without blocking service.
- Added and updated the architecture, security, protocol, recovery, source
  reconciliation, save-overlay, performance, deployment, dashboard, and
  support documentation. `/library` remains read-only, Compose hardening and
  non-root/read-only-root execution remain unchanged, and no full GameCube
  image or game payload is copied.
- Managed save directories are created one checked component at a time, so a
  pre-planted `individual` or `shared` symlink cannot redirect creation.
  Active writable extents are validated against the generation, layout
  checksum, object identity, and card size before backend activation.

### Validation

- `make test`: PASS across Host, NBD, Pi, shared packages, FAT32, integration,
  and utility suites.
- `make static`: PASS, including builds, `go vet`, shellcheck, and Pi static
  validation.
- `make compose`: PASS (`static compose policy: PASS`; Docker Compose parser
  PASS).
- `make oci`: PASS;
  the final clean implementation commit `7097d8a` produced
  `wiibridge-host:0.1.0-rc.1@sha256:f34bd8d2036d918dd2ab841e53cbf6b924369c8dfbaa4b375bc1f142fe2b0d4e`.
- `go test -race ./server/host-daemon/... ./server/nbd-plugin ./shared/... ./pi/controller/...`:
  PASS.
- `go vet ./server/host-daemon/...` and `git diff --check`: PASS.
- The current controller cross-builds with `CGO_ENABLED=0 GOOS=linux
  GOARCH=arm GOARM=6` as a stripped, statically linked 32-bit ARM EABI5
  executable; SHA-256
  `5418fd2008debd021c8c55d853eedc6ce80b01c41a01795d7f97618841c74857`
  from clean implementation commit `7097d8a`.
- `go mod tidy` could not traverse pre-existing root-owned Pi build-root files
  below `build/pi-gen-zero-w-armhf`; it stopped with permission denied and did
  not change `go.sum`. Targeted module builds, all tests, `go vet`, and static
  validation pass with the intentional direct `golang.org/x/sys` declaration.
- The full Pi image was not rebuilt because doing so would delete and recreate
  the existing 1.2 GiB ignored pi-gen work tree. The affected ARMv6 controller
  and packaging metadata were validated directly.

### Measured overhead and remaining validation

- `reports/four-feature-performance.json` records median 64 KiB GameCube
  backend reads of 3,099 ns with telemetry disabled and 3,239 ns enabled:
  +140 ns or 4.52%, with the same 288 B/op and two existing allocations.
  The in-memory NBD transmission microbenchmark measured +90 ns (9.55% in
  the deliberately storage/TLS-free fixture), again with no added allocation.
  Enabled counter observation was 61.47 ns/op with zero allocations. No
  per-read persistence, blocking telemetry network call, or unbounded label
  set exists.
- Cached Pi snapshot access measured 31.42 ns/op with zero allocation; the
  authenticated metrics endpoint measured 2,205 ns/op on amd64. These are not
  Pi Zero W measurements.
- Physical GameCube/Nintendont save creation, flush, reconnect, restore, and
  sustained I/O remain `DEFERRED_HARDWARE_UNAVAILABLE`. Pi Zero W telemetry
  cost, current firmware-image boot, and deployment of the clean `7097d8a`
  Host OCI image to TrueNAS also remain unverified. Prior physical Wii launch,
  read-only NBD, and Pi recovery evidence is retained but was not repeated for
  this implementation.

## 2026-07-28 — Current Pi Zero W full firmware image

- Started from a clean worktree at published commit
  `c60d4b500944044a581e039570d3a8432b1921e2` and rebuilt the complete
  `zero-w-armhf` pi-gen image. The controller was linked with product version
  `0.1.0-rc.1`, that exact revision, build time `2026-07-28T22:59:31Z`, and
  `dirty=false`.
- Used pinned pi-gen commit
  `314262cb286b8f33327a6f0cbabe14c625021ca0`. The resulting controller is a
  stripped, statically linked 32-bit ARM EABI5 executable with SHA-256
  `c380f33b36f6408c46619e81f07585fe1b2f7e77e5a4225c5736988618b02b01`.
- Generated the 2,751,463,424-byte flashable image and 535,258,860-byte XZ
  archive. Image SHA-256 is
  `89167db6b8dcd7e544589e90306f36e42c9066779430b5964ef19073f6a3f8e5`;
  compressed SHA-256 is
  `2873476d07fa63f5b3b3ef25cf71379b0179322297f1a1915aca860a765a3e73`.
  Both checksum files pass, `xz --test` passes, and streaming decompression
  reproduced the original image checksum.
- Offline validation passed partition layout, non-destructive FAT/ext4 checks,
  read-only inspection, Zero W board metadata and boot files, `dwc2` gadget
  configuration, required services, NBD preload/module presence,
  `libcomposite` and USB mass-storage gadget modules, QEMU ARM application
  smoke, no game payloads, and no embedded machine/device identity.
- Generated a bmap, 643-package manifest/SPDX SBOM, provenance, retained build
  log, and offline validation report. The first combined shell was externally
  terminated with `SIGTERM` only after the image and offline validation had
  completed, while XZ compression was running. The incomplete archive was
  replaced by a successful standalone packaging pass; the final hashes above
  were independently rechecked.
- The packaging script's existing provenance template conservatively records
  `sourceWorktreeDirty:true` even though the build log and preflight confirmed
  the worktree was clean before image construction. This metadata limitation
  does not change the embedded revision or image hashes.
- Flashing, first boot, authenticated descriptor/metrics collection, USB
  gadget enumeration, and performance measurement on the physical Pi Zero W
  remain `DEFERRED_HARDWARE_UNAVAILABLE`.

## 2026-07-30 — USB Loader GX large-library freeze diagnosis and repair

- Captured the live failure while USB Loader GX remained frozen. The Pi Zero W
  reported `ready`, NBD connected, USB attached/configured, export profile
  `wii`, zero USB resets, zero request failures, and no increase beyond 74
  completed requests during an 11-second sample. The transport was healthy and
  the Wii stopped issuing block reads.
- Attached the live mutual-TLS NBD export read-only for diagnosis. It was
  1,816,313,603,072 bytes with an MBR FAT32 LBA partition, but its BPB fixed
  sectors-per-cluster at 8. The resulting approximately 442.6 million data
  clusters exceed FAT32's usable 28-bit cluster-number range and cross into
  reserved values. This explains the initialization stall before payload I/O.
- Confirmed the current Pi controller and Host both identify as revision
  `c60d4b5`. The earlier Pi controller update did not change the NBD helper,
  gadget setup, USB auto-attach unit, or controller unit, eliminating that
  update as the storage regression.
- Reworked the Wii virtual-disk builder to select a power-of-two cluster size
  dynamically. Existing small disks retain their 4 KiB geometry. Large disks
  increase cluster size only as needed to remain under the last usable FAT32
  data-cluster number and within MBR and 32-bit LBA capacity.
- Added a synthetic 1.65 TiB-scale regression. It selects 16 sectors per
  cluster (8 KiB), exposes approximately 221 million clusters, keeps the FAT
  compact/on-demand, and builds without opening a payload source.
- `make test`, `make static`, `make compose`, targeted race tests,
  `git diff --check`, the hardened Docker lifecycle test, and OCI inspection
  pass. The local dirty-worktree OCI manifest digest is
  `sha256:7150b81412b643c99cf996247371d4098529a4355dd9a688b743439c5e5d31fe`;
  it is diagnostic evidence, not a published clean release.
- The live TrueNAS container has not been replaced because this workspace has
  HTTPS/NBD client access but no authenticated TrueNAS management shell or
  API. Physical USB Loader GX retesting remains pending that deployment.

### Valid FAT32 deployment and physical follow-up

- Published and deployed the first geometry repair at commit `a0b2023`. The
  replacement export is 1,814,543,805,440 bytes, uses 8 KiB clusters, stays
  below FAT32's usable cluster-number limit, identifies as FAT32 through
  `blkid`, and exposes the root and 987-entry `/wbfs` directory through
  independent `mtools` reads.
- The physical Wii retest still froze at `Initializing USB devices`. During
  the freeze the Pi remained ready, NBD-connected, USB attached/configured,
  and error-free. Completed NBD requests remained at 60 across a 22-second
  sample, with no request failure, reconnect, or USB reset.
- Confirmed against USB Loader GX r1283 source that this screen covers USB
  spin-up and `MountAllUSB`, including partition parsing and the libfat mount,
  before the startup message advances. The absence of continued NBD traffic
  localizes the failure to initial storage discovery/mount rather than a slow
  remote directory scan.
- Changed the large-library policy to use 32 KiB FAT32 clusters at 32 GiB and
  above while preserving the proven 4 KiB layout below that threshold. The
  1.65 TiB-scale synthetic regression now exposes approximately 55.3 million
  data clusters and a 432,182-sector FAT per copy; the 8 GiB regression still
  uses 4 KiB clusters.
- `make test`, `make static`, `make compose`, the targeted race test,
  `git diff --check`, the hardened Docker lifecycle test, and OCI inspection
  pass. The local dirty-worktree OCI manifest digest is
  `sha256:2541660dbf0085b0ccad6f9294ed4446f9ece6aa74ab07efcdb97c4ba71f1789`;
  it is validation evidence, not a clean published release.

### 32 KiB physical failure and FSInfo mount-scan diagnosis

- Published commit `2a7383a` and deployed immutable GHCR digest
  `sha256:5538addc6e525d0ffac405192c97a3ef955eef4d9974e327e22e3e070f487b3f`.
  The live Host identifies as that clean revision and reports ready.
- Independently attached the live mutual-TLS NBD export read-only. It is
  1,813,217,565,696 bytes with a 3,541,438,510-sector MBR partition, 32 KiB
  FAT32 clusters, and a 432,199-sector FAT per copy. This proves that the
  intended image and geometry were active for the physical retest.
- The retest still froze at `Initializing USB devices`. The Pi remained ready,
  NBD-connected, and USB configured. Completed NBD requests stayed at 143
  across a 40-second sample with zero read failures, reconnects, USB resets,
  or recent errors.
- Read the deployed primary FSInfo sector directly and confirmed that both the
  free-cluster count and next-free hint are `0xffffffff`. USB Loader GX r1283
  bundles custom libfat 1.1.5. Its exact source treats the unknown free-count
  sentinel by calling `_FAT_updateFS_INFO`, which invokes
  `_FAT_fat_freeClusterCount` and walks the entire FAT as part of `fatMount`.
  The synthetic builder has always emitted these unknown sentinels.
- Changed geometry generation to retain at least one real free cluster and
  write exact, nonzero free-cluster and next-free values to both FSInfo copies.
  The live-scale regression verifies the values against the compact FAT chains
  and proves that the advertised next-free cluster is actually unallocated.
- `make test`, independent FAT32 validation, `make static`, `make compose`,
  the targeted race test, `git diff --check`, hardened Docker lifecycle, and
  OCI inspection pass. The local dirty-worktree OCI manifest digest is
  `sha256:93d6056a9ce0dab0030a55e7a74abf76c7ee64f564c8899b430fbb50cc350ce8`;
  it is validation evidence, not a clean published release.

### Exact FSInfo deployment and resource-stage observation

- Published commit `8e8ec40` and deployed immutable GHCR digest
  `sha256:aa1a5a6db11320c504daed2c8b198b1fb8bc6836c3a5ef90fb5b4c4adc44707f`.
  The live Host reports that clean revision and Ready.
- Independently verified the live read-only export at block level. It is
  1,813,217,599,488 bytes with 32 KiB clusters and a 432,200-sector FAT per
  copy. FSInfo reports exactly one free cluster, points to cluster 55,321,472,
  and the corresponding FAT entry is zero.
- USB Loader GX advanced beyond `Initializing USB devices`, physically
  confirming that the exact-FSInfo repair removed the prior mount blocker.
- The next visible screen remained `Loading resources`. Pi telemetry proved
  that the Wii was actively traversing the WiiBridge catalog: completed NBD
  requests rose from 4,251 to 4,908 over 27 seconds at approximately
  1.2-1.5 MB/s, then reached 5,518 and stopped. Pi CPU fell from 74% to below
  1%, with zero NBD read failures, reconnects, USB resets, or recent errors.
- USB Loader GX r1283 leaves the `Loading resources` startup text visible
  after resource initialization while `MainMenu` calls
  `MountGamePartition`, which enumerates `/wbfs` and reads each game header
  before resuming the GUI. The observed finite I/O matches that catalog path.
  The loader remained on that stale screen after the traversal completed,
  localizing the current failure to USB Loader GX's post-enumeration
  game-list/cache/UI path rather than WiiBridge transport, USB mounting, or
  FAT reads. A reversible reset of the loader's SD-resident configuration and
  caches is the next isolation step.

### Wii SD repair and clean-cache isolation staging

- Identified the connected removable card as the 59.5 GiB FAT32 Wii SD card
  containing USB Loader GX r1283.
- The pre-change read-only filesystem check found a dirty bit, divergent FAT
  copies, and an incorrect free-cluster summary. Preserved a verified
  3,815-entry allocated-file archive, partition table, and raw MBR/boot/FAT
  prefix before repair. `fsck.fat` selected the intact first FAT, wrote the
  repair, and the final unmounted read-only check reports no inconsistency.
- Audited the loader caches before moving them aside. `WII.cache` is version
  1282 with 928 unique, structurally valid Wii records. `TitlesCache.bin` is
  version 1283 with 934 unique, sorted, structurally valid title records, and
  covers every cached Wii ID. `GameTimestamps.txt` contains 938 unique IDs;
  its ten non-Wii-cache entries are GameCube/channel records rather than
  evidence of a truncated Wii cache.
- Retained the previous loader files in the on-card
  `apps/usbloader_gx/wiibridge-backup-20260730T040537Z` directory and in the
  verified host recovery archive. No prior state was deleted.
- Staged an isolation configuration that retains list view but changes title
  source to disc headers, limits the source mask to Wii games, disables header
  and banner caches, skips the cache CRC directory pass, and disables the
  free-space calculation. The active header/title/timestamp caches were moved
  aside so the next boot cannot reuse them.
- Synchronized, unmounted, checked, and powered off the SD card. It is safe to
  remove. The physical clean-cache boot remains pending.

## 2026-07-31 — Physical-regression continuation and WBFS split diagnosis

- Preserved the four existing uncommitted investigation/status files, fetched
  both remotes without resetting, and created
  `agent/diagnose-physical-regressions` at GitHub `main` merge commit
  `8e8ec4014e9d2bf0e85d63cad3241f4e39613c33`. The prior branch content tree
  was byte-identical to that merge commit.
- Recovered and read the deleted controlling project specification from commit
  `082aae0`; `AGENTS.md` still names the file although commit `ad08074` removed
  it from the current tree.
- Reverified live identity without relying on deployment YAML. Host
  `/healthz` reports clean revision `8e8ec401`, build time
  `2026-07-30T03:39:38Z`, and healthy/Ready. The local pulled OCI resolves to
  published digest
  `sha256:aa1a5a6db11320c504daed2c8b198b1fb8bc6836c3a5ef90fb5b4c4adc44707f`
  with the matching revision label. The Pi reports clean firmware revision
  `c60d4b5`, Zero W Rev 1.1, NBD connected, USB configured, zero read failures,
  reconnects, and USB resets. TrueNAS management-plane access is unavailable,
  so configured/running container digest, container ID, restart count, exact
  TrueNAS version/backend, and host process/container counters remain
  unverified rather than inferred.
- Attached a second client to the live NBD export with the existing mutual-TLS
  identity and read-only flag. Verified the 1,813,217,599,488-byte disk and
  1,813,216,550,912-byte partition: MBR signature/type/range, deterministic
  nonzero disk signature, 512-byte sectors, 32 KiB clusters, two 432,200-sector
  FATs, 55,321,471 data clusters, valid primary/backup boot sectors, exact
  matching primary/backup FSInfo values of one free cluster and next-free
  55,321,472, and a zero FAT entry at that next-free cluster.
- Corrected a local comparison-harness `sudo`/process-substitution error and
  reran the checks: primary/backup boot sectors match, both FSInfo sectors
  match, and the two complete FAT copies stream-compare equal. `blkid` detects
  FAT32 with 32 KiB filesystem blocks. `mtools` reads root and `/wbfs` and
  enumerates 987 virtual segments. `mattrib` reports archive set on all 987,
  with read-only, hidden, and system attributes clear on all 987. This rejects
  a live recurrence of the prior DOS read-only attribute regression.
- Independent read-only `fsck.fat -n` found that virtual segments sized
  4,294,963,200 bytes have apparent zero-byte FAT chains. Targeted `mshowfat`
  proved the chains are present and contiguous. The boundary explains both:
  with 32 KiB clusters the current `4 GiB - 4 KiB` segment rounds up to exactly
  2^32 allocated bytes, which wraps in 32-bit FAT chain-length accounting.
  The live directory contains 59 such incompatible segments.
- Audited USB Loader GX r1283 commit `e25c4f3`. Its split implementation uses
  `4 GiB - 32 KiB`, documented in source as one cluster below 4 GiB, and opens
  existing split parts with `O_RDWR`. The live archive attributes permit that
  open, but WiiBridge's segment boundary did not match the loader's own
  compatibility boundary.
- Added the regression before changing the builder. It failed with the current
  4,294,963,200-byte segments and reproduced the independent `fsck.fat`
  zero-chain result on a compact sparse 32 GiB fixture. Changed only the
  segment maximum to 4,294,934,528 bytes. The same regression then passed.
  Additional focused coverage verifies ordered `.wbfs`/`.wbfN` parts,
  archive-only attributes, LFN checksums, unique short aliases, directory
  sizes, chains below 2^32 bytes, exact simulated banner-range reads, and an
  exact read crossing the virtual split boundary.
- Physical catalog, banner, launch, and sustained-play results remain pending.
  The corrected builder has not yet completed full validation, clean OCI
  publication, TrueNAS deployment, live snapshot regeneration, or Wii retest.

### Split-boundary validation and deployment identity gate

- Added Host capability `wii-fat32-exact-fsinfo-split-v1` so a deployment can
  prove that the running binary contains the required filesystem format rather
  than relying only on a version tag.
- Added `deploy/truenas/verify-runtime-identity.sh`. It fails closed on an
  unpinned/mismatched configured digest, running registry digest mismatch,
  OCI/binary/API revision mismatch, dirty build, missing format capability, or
  failed health/readiness. The administrator token is read into a mode-0600
  temporary curl configuration and is not printed.
- Added an automated deployment-check test. The exact clean mock passes;
  digest mismatch and missing format capability are both rejected.
- Validation passes: `make test`, `make static`, `make compose`,
  `go test -race ./server/host-daemon/...`,
  `go vet ./server/host-daemon/...`, `go test -v ./tests/fat32`,
  `./tests/truenas/container-test.sh`, `make oci`, JSON validation, and
  `git diff --check`. Dirty diagnostic container image ID is
  `sha256:2b5b4bf0024e72ff9de2f2b21bcc4e2487c26cfdd575d24152609643b52f33da`;
  dirty diagnostic OCI manifest is
  `sha256:5ccac175a103c8d644edbae7a36f1ff2da5539b8afc3194feb48fff3e8ac5ffe`.
  Neither is a release candidate.
- Focused benchmark ranges on the Ryzen 9 9950X3D build host: 512 GiB FAT
  synthesis 244-250 microseconds/op; FAT sector generation 640-664 ns/op;
  Wii 1 MiB cached source read 29.4-29.8 microseconds/op; 10,000-extent lookup
  10.13-10.20 ns/op; NBD 64 KiB read 936-952 ns/op with metrics disabled and
  1,039-1,043 ns/op enabled; atomic enabled observation 62.01-62.45 ns/op with
  zero allocations; cached Pi sample 31.64-31.89 ns/op with zero allocations.
  These are host-cache measurements, not physical Pi/Wii throughput.

### Lifetime Pi management certificate correction

- Traced Host revision `15b0e40` to a legacy Pi first-boot certificate with a
  30-day validity window and a Host-side date rejection; this failure path is
  consistent with the observed 2026-08-28 startup timing.
- Changed new Pi device identity generation to use the X.509-required
  `notAfter` field with a 100-year lifetime (`36525` days).
- Kept exact DER certificate pinning and the independent Pi management token,
  but made the pin—not certificate validity dates—the lifetime trust anchor.
  This lets existing exact-pinned 30-day device certificates remain usable
  without rotating the Pi private key or copying a replacement pin.
- Added regressions proving an expired exact-pinned TLS identity works and an
  expired mismatched identity is still rejected.
- Validation passes: focused bridge-control tests, `make test`, `make static`,
  shell/static certificate-generation policy, and `git diff --check`.
- Source correction is verified locally; publication, deployment, and physical
  retesting remain pending, and the running `15b0e40` image is unchanged.

### Lifetime certificate Host publication

- Merged pull request #11 to `main` as `7fd0c3ae8ef4318e11c1b36595cf3e5c40eaaa92`.
- GitHub Actions run `33214221649` passed binary/OCI identity checks and
  published both the release and commit-specific GHCR tags.
- Independent daemonless registry inspection resolved both tags to
  `sha256:4df501302aee625f50f125b5e21c4f3c7402408cd40cdfebaab8d7991227749b`
  and confirmed Linux/AMD64, version `0.1.0-rc.1`, and the exact main revision.
- Pinned the immutable commit tag and digest in the generic TrueNAS Compose,
  environment, and paste-ready YAML templates. Runtime deployment remains
  pending and is not recorded as passed.

### Separate Wii/GameCube library paths and moved-mount recovery

- Confirmed the reported rescan failure matches source preflight rejecting a
  different saved mount identity. The existing retry path had no explicit
  recovery operation for an intentionally moved library.
- Added `WIIBRIDGE_WII_LIBRARY` and `WIIBRIDGE_GAMECUBE_LIBRARY`, both falling
  back to the existing shared `WIIBRIDGE_LIBRARY` setting. Separate roots
  retain independent catalog availability, scan controls, runtime failure
  handling, and diagnostics. Shared-root rescans remain atomic.
- Added an authenticated, CSRF-protected, explicitly confirmed location
  recovery action. Read-only proof, complete discovery, non-empty valid-game
  checks for previously populated libraries, mount rechecks, and snapshot
  validation precede an atomic SQLite identity/catalog/snapshot commit.
  Failed scans preserve the old trusted mount, catalog, and save data.
- Kept fresh GameCube scan results playable when an old generation refers to
  moved paths. Generations built for another configured root cannot activate;
  new builds use the selected GameCube root. An unavailable Wii source no
  longer prevents a healthy GameCube library from building or activating.
- Added per-platform dashboard source/recovery cards, a standalone separate-
  dataset Compose definition, updated read-only mount validation/preflight,
  and `docs/library-locations.md`. Hosts advertise `separate-library-paths-v1`.
- Validation PASS: `make test`, `make static`, full Host/source-health race
  checks and focused final race checks, shared/separate Compose parsing, a
  negative writable-second-mount check, V8 dashboard JavaScript syntax,
  public-tree privacy checks including new files, and `git diff --check`.
- Real container validation PASS via
  `tests/truenas/separate-libraries-test.py`: separate read-only mounts;
  moved GameCube file rescan/rebuild; persisted stale mount rejection then
  explicit recovery; empty replacement preserving the catalog and Wii
  readiness; GameCube build/activation with Wii unavailable; unchanged
  synthetic source bytes. An initial harness run was corrected to wait for
  the new build to finish instead of the prior generation's ready flag.
- Local build is intentionally marked dirty/unpublished. No live TrueNAS
  configuration, games, database, certificates, or saves were changed.
  Publication, operator dataset paths, deployment, and physical retesting
  remain pending; physical results are `DEFERRED_HARDWARE_UNAVAILABLE`.

## Full performance audit — 2026-09-12, implementation underway

- Freshly cloned and resolved remote main to
  `cbb1d7842d866a7ba3969a2a8ae39679ccd5cd27` (2026-08-28). Preserved that
  checkout, generated artifacts, logs, and exact local environment inventory.
- Baseline PASS: `make test`, `make static`, `make server`, `make compose`,
  `make oci`, full server/Pi/shared/tests race run, vet, and `make release`.
  The release built all three firmware targets and passed offline validation,
  including filesystem checks and QEMU application smoke tests. Release elapsed
  time was 4033.057 seconds, with audit tooling also running during the build.
- Additional baseline PASS: hardened OCI startup/restart, real read-only binds,
  libnbd mutual TLS, plaintext rejection, matching FAT copies through NBD,
  Linux NBD read-only mounts, and synthetic Wii/GameCube payload checksums.
  Temporary containers and the audit-loaded NBD module were removed afterward.
- Repeated benchmark comparison uses ext4 temporary storage after firmware
  building finished. Exploratory Go fixtures used the machine's RAM-backed
  temporary filesystem; their timings are not representative of disk storage.
  Allocation and syscall-count findings were retained separately. Cold source
  page tests now verify eviction with mincore; storage caches remain uncontrolled.
- Opened a separate clean implementation worktree on
  `perf/full-system-optimization`. Preserved the previously requested separate
  Wii/GameCube paths by cherry-picking the existing implementation, separately
  from performance changes and performance claims.
- Implemented indexed Wii extent lookup, allocation-free batched FAT synthesis,
  and complete NBD reply framing with short-read rejection. Focused vdisk,
  protocol, and mutual-TLS NBD tests PASS. Remaining implementation and the
  optimized full release validation are pending.
- No operator game data, live TrueNAS deployment, Pi configuration, or Wii
  settings were changed. Physical launch and save tests remain
  `DEFERRED_HARDWARE_UNAVAILABLE`.

## September full-performance implementation and validation

Implemented logarithmic Wii extent lookup and allocation-free FAT sector reads;
coalesced NBD response framing with strict short-read handling; compact GameCube
FAT storage and non-copying validation; bounded cancellation; coalesced save
reads; prepared SQLite reconciliation; dashboard pagination and visible-only,
non-overlapping periodic polling. Durable writes and security gates remain.
Corrected benchmark cache/fixture labels and release build-input provenance.

All seven requested optimized software checks pass, including the full race
suite. Six-sample ext4 measurements, preserved-baseline generation checks,
actual readonly Docker/kernel NBD mounts, payload hashes, fsck without repairs,
and separate-library recovery/activation integration pass. Physical results
remain unavailable. The final clean release build and publication are pending.
See docs/full-performance-audit.md and reports/performance/2026-09-full-audit.

## Firmware build isolation correction

The first concurrent release firmware attempt at 18346a0 failed: Pi 4 and Pi 5
pi-gen jobs could not unmount shared chroot device filesystems. Stopped only the
remaining audit builder's descendants; normal unmount cleanup removed all ten
remaining audit mounts and restored the original host devpts mount topology.
No failed firmware was published. Added private mount namespaces per builder
and a mount-presence guard before build-tree deletion. Final validation and a
fresh full build of every board remain pending after this correction.

## Validated performance prerelease publication

Completed the clean release at `3c5dd917cfe0d6771cdd2e003fa9002dfcb963f2`: full software validation,
all three complete firmware builds, repeated offline checks and final packaging
PASS. The corrected parallel firmware build took 1,884 seconds; no controlled
build-speed claim is made. Parent mount inspection found no remaining pi-gen
mounts. Final Wii/GameCube hardened OCI, mTLS/libnbd/Linux NBD, readonly payload
hashes, both FAT copies, denied writes, fsck and separate-source recovery PASS.

Published immutable GHCR digest `sha256:d345997ca9271e5451b354afe646464a35810e561ae1ffa3dc7fc8016a25cf09` and
[the prerelease](https://github.com/mhilton7/WiiBridge/releases/tag/perf-2026-09-13-3c5dd91). All 30 uploaded sizes and SHA-256 values were
verified before publication. A draft-release lookup by tag returned 404 after
upload; verification resumed using the existing draft's release ID, without
reuploading or replacing assets. Final public lookup passed.

Finalized separate-library YAML after binary packaging: remove the nested
required shared-path fallback, require both dedicated paths, reject empty/missing
paths, validate every shipped Compose definition and replace the obsolete GHCR
example with the tested image pin. These deployment and audit records do not
change packaged host/Pi runtime code. The manifest records its exact revision.

Baseline/main is preserved; work is on `perf/full-system-optimization` and PR 14.
Physical board boots, Wii launches, TrueNAS/ZFS behavior and emulated-save
hardware qualification remain `DEFERRED_HARDWARE_UNAVAILABLE`. No operator
repository was reset and no live appliance deployment was performed.
