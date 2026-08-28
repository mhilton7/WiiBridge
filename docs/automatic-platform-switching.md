# Automatic platform switching

WiiBridge can coordinate the existing Host export manager and Raspberry Pi
controller. This removes the manual detach/disconnect/reconnect sequence while
keeping Wii and GameCube as isolated storage profiles. It does not add a title
changer and does not modify USB Loader GX or Nintendont.

## Safety sequence

Every automated switch is serialized and follows this order:

```text
Pi detach USB
→ Pi disconnect NBD
→ Host waits for active I/O to close
→ Host validates and selects the requested export
→ Pi reconnects NBD in the matching read/write mode
→ Pi validates that mode
→ Pi attaches USB
```

For platform transitions, the Host permits only `detach`, `disconnect`, `connect-wii`,
`connect-gamecube-physical`, `connect-gamecube-emulated`, and `attach`. It
cannot invoke provisioning, cache deletion, or arbitrary commands. Separate,
explicitly confirmed power controls may invoke only the typed `reboot` and
`poweroff` helpers.

If preparation or disconnection fails, the Host export is not changed. If
reconnection or attachment fails, WiiBridge performs a best-effort detach and
disconnect and reports that USB remains detached. It never switches the backing
store while the Wii is connected.

When automatic coordination is configured, the Host disables both library
activation controls unless the most recent Pi status reports a compatible,
provisioned board with an available USB gadget controller. Every activation
also performs a fresh authenticated status probe before the detach sequence.
If the Pi is powered off, unreachable, still in setup, on the wrong board, or
missing its gadget controller, no Pi action runs and the Host export remains
unchanged. A Pi that disappears after the probe is still caught by the existing
detach-first sequence before export selection.

## Enable it

Automatic switching is deliberately opt-in. On the Pi, identify its management
URL and copy only its public management certificate:

```sh
sudo sha256sum /etc/wiibridge/device.crt
sudo cp /etc/wiibridge/device.crt /path/to/removable-or-secure-transfer/pi-device.crt
```

Do not copy `device.key`. Place `pi-device.crt` in the TrueNAS certificate
dataset and compare its SHA-256 with the value recorded on the Pi.

X.509 certificates must contain a `notAfter` value, so newly provisioned Pi
devices use a 100-year validity period. WiiBridge treats the exact pinned DER
certificate as the device identity and does not expire that identity based on
the certificate's validity dates. This also keeps earlier devices with the
legacy 30-day certificate operational after its date has passed. Rotate the
certificate manually if the Pi is reprovisioned or its private key might have
been exposed; never replace it merely to extend its date.

Set the token and pinned certificate in `deploy/truenas/.env`. The URL is an
optional initial address; when it is blank, enter the Pi's literal IP address
from the authenticated dashboard:

```text
WIIBRIDGE_PI_URL=
WIIBRIDGE_PI_ADMIN_TOKEN=THE_EXISTING_PI_MANAGEMENT_PASSWORD
WIIBRIDGE_PI_CERT=/certs/pi-device.crt
```

The token is the Pi management password created during first boot, not the Host
administrator token. Keep it out of Git. Restrict the Host app's network access
to the single Pi address and TCP 9443.

Restart only the Host app after reviewing the configuration. The dashboard
will show “Automatic safety sequence enabled.” The token and certificate are
required. If the URL is blank, status and controls remain offline until an
administrator saves the Pi IP in the dashboard. The management port remains
fixed at 9443 and the pinned certificate remains mandatory.

## Operation

Build the complete GameCube generation before switching. Then click **Activate
GameCube Library** or **Activate Wii Library**. The dashboard reports completion
only after the Pi has reattached USB. USB Loader GX may need its normal device
refresh after USB re-enumeration.

All Wii titles remain together in the proven Wii catalog. All prepared
GameCube titles remain together in the generated GameCube FAT32 catalog.
Primary switching never requires a title ID.

The Host also provides typed storage controls: **Safely Detach USB**,
**Disconnect NBD**, **Connect Current Library**, **Attach USB**, and
**Reconcile Connection**. Connect mode is derived from trusted Host state,
never from a browser-provided helper action.

## Pi power controls

When automatic coordination is configured, the Host dashboard displays a
collapsed **Pi power controls** panel. Reboot and shutdown each require their
own confirmation checkbox and an authenticated CSRF-protected POST request.
Both actions run the Pi's fixed helper, which:

1. detaches the USB gadget;
2. disconnects NBD;
3. calls `sync`;
4. requests either `systemctl reboot --no-block` or
   `systemctl poweroff --no-block`.

The API equivalents are:

```text
POST /api/v1/pi/reboot    confirm=reboot
POST /api/v1/pi/shutdown  confirm=shutdown
```

These controls require a Pi firmware/controller package containing the typed
`reboot` helper and its exact sudoers rule. Shutdown uses the existing typed
poweroff helper. No arbitrary action name or command text is accepted.

## Live Pi status

When automatic coordination is configured, one Host background worker requests
fresh Pi state every ten seconds. Dashboard browsers request
`GET /api/v1/pi/status`, which returns only the cached result; opening more
dashboard windows never creates more Pi traffic. Page rendering does not wait
for the Pi. The Host retrieves each fresh result through the same
certificate-pinned HTTPS connection and independent Pi administrator token used
for switching. The browser never receives that token or the pinned certificate.

The panel reports controller and provisioning state, export mode, NBD
connection, USB attachment and controller state, detected board, network
addresses, and automatic-attach state. If the Pi is offline, authentication
fails, its certificate does not match, or the request times out, the panel
changes to `Unavailable` and retries. The dashboard receives a deliberately
generic error instead of transport or credential details.

The dashboard also provides an authenticated, CSRF-protected Raspberry Pi IP
address control. It accepts only a literal IPv4 or IPv6 address, always uses
HTTPS port 9443, and retains the existing pinned certificate and administrator
token. The address is stored with mode `0600` under the Host data directory.
Arbitrary URLs, hostnames, ports, paths, and credentials are rejected.

## Disable or recover

To return to manual operation, clear all three `WIIBRIDGE_PI_*` values and
restart the Host app.

After a reported failure, inspect the Pi dashboard on port 9443. Confirm USB is
detached before using its fixed recovery actions. Wii remains the startup and
recovery default after an ordinary reboot.

The Pi management certificate is pinned exactly and remains trusted for the
lifetime of that device identity, regardless of its encoded validity dates.
When the certificate is rotated or the Pi is reprovisioned, copy and verify the
new public certificate before updating `WIIBRIDGE_PI_CERT`. A certificate
mismatch stops automation; it never falls back to unauthenticated TLS.
