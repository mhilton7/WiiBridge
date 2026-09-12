# Host dashboard

The Host dashboard is the normal WiiBridge control center. On a fresh data
directory, sign in over HTTPS with:

```text
Username: admin
Password: wiibridge
```

The first login is restricted to the password-change page. WiiBridge stores
only a salted Argon2id record in `/data/auth/password.json` (directory mode
`0700`, file mode `0600`) and invalidates every browser session after a change.
Sessions are server-side and use a Secure, HttpOnly, SameSite=Strict cookie.
Existing Bearer and Basic API authentication continues to use
`WIIBRIDGE_ADMIN_TOKEN`; it is never placed in a browser cookie.

During Host startup, HTTPS opens before the potentially long Wii and GameCube
library scans. The browser displays a small auto-refreshing startup page with
the current phase, phase start time, elapsed time, last completed phase, and a
bounded failure summary. `GET /healthz` reports `starting` as soon as HTTPS is
live. `GET /readyz` remains HTTP 503 and NBD remains unavailable until the
validated Wii virtual disk and export manager are ready. Docker logs emit each
phase transition, scan counts, mapped-file counts, elapsed milliseconds, and a
30-second heartbeat while a phase is still running.

Wii readiness does not wait for a full GameCube source hash. On an upgrade from
a generation without a current validation receipt, the authenticated dashboard
becomes available with **GameCube library: Validating** while a cancellable
background validator reads the GameCube sources. GameCube activation remains
disabled until that validation succeeds and its compact receipt is committed.
Routine restarts reuse the receipt only while all stored source identities and
generation checksums still match.

The two primary controls are:

```text
Activate Wii Library
Activate GameCube Library
```

The Wii control always selects the complete proven Wii snapshot. The GameCube
control selects the current complete validated generation and is disabled until
one exists. Building and updating are separate background operations; opening
the page never starts a build.

The Wii and GameCube source cards show each configured folder and provide
separate rescan controls. **Moved this library?** validates and accepts an
intentionally replaced mount. See [library locations](library-locations.md)
for the environment settings and TrueNAS bind mounts.

The dashboard also has four integrated status areas:

- Source reconciliation preserves the prior catalog when the dataset is
  offline and distinguishes availability from validation.
- Compatibility displays Host/Pi descriptors, protocol overlap, capabilities,
  warnings, and operation blockers; Retry performs a fresh authenticated probe.
- Save Overlay manages physical/individual/shared modes, card creation,
  verification, backup history, restore, upload, and download.
- Performance displays the observable Source → Host → NBD/TLS → Pi → USB path
  and bounded session history. Missing Pi metrics does not affect serving.

The USB state is electrical/configuration state, not a progress counter. A
`configured` gadget with a connected NBD session can still be idle because USB
Loader GX or a game stopped issuing commands. Diagnose a freeze by comparing
timestamped NBD completed requests, source-read counters, throughput, failures,
reconnects, and USB resets. A flat request count with zero errors is not a
TrueNAS throughput failure.

Source acknowledgement, save restore/upload, mode changes, switching, and
power actions use the existing authentication, per-session CSRF, and explicit
confirmation conventions. Save mode changes and restore are unavailable while
the Wii owns the GameCube export.

Raspberry Pi storage and power controls are grouped inside the **Raspberry Pi
bridge** status panel. Reboot and shutdown retain their required confirmation
checkboxes. The **Catalog Viewer** below that panel is collapsed by default and
can be expanded to search or filter the complete Wii and GameCube source
catalogs. Its open state and page position survive dashboard refreshes, and it
opens automatically for a search, a platform filter, or rejected-source review.

GameCube builds report titles, discs, mapped files, current phase, generated
metadata bytes, extent count, and validation rather than payload-copy bytes.
The generated FAT32 disk reads game
payloads through to `/library`; `/data` contains compact metadata only. The
dashboard reports **Legacy copied GameCube generation detected** when an older
schema-1 `library.img` remains, but it does not delete it automatically.

The Pi panel uses the existing pinned management certificate and independent Pi
token. It exposes only typed operations: detach USB, disconnect NBD, connect the
current trusted profile, attach USB, reconcile, reboot, and power off. It does
not expose a shell, arbitrary helper actions, provisioning, firmware flashing,
private keys, or filesystem access.

The sign-in and password-change forms use standard browser password-manager
fields, so Chrome can offer to save and update the WiiBridge username and
password. Open the dashboard through its normal HTTPS address with a certificate
Chrome trusts; Chrome may suppress credential saving on a connection opened
through a certificate-warning bypass.

## Password recovery

1. Safely detach USB and stop the Host container.
2. Back up `/data/auth/password.json`.
3. Remove only `/data/auth/password.json`.
4. Restart the Host.
5. Sign in with `admin` / `wiibridge` and immediately replace it.

This does not alter API/Pi tokens, certificates, games, generated metadata, or
save backups.
