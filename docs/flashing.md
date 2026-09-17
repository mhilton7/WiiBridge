# Firmware flashing

Select the artifact whose target exactly matches the board. Verify both the
`.img.xz.sha256` file and the release provenance before writing it. Use
Raspberry Pi Imager's custom-image option, or:

```sh
xz -dc wiibridge-VERSION-TARGET.img.xz |
  sudo dd of=/dev/EXPLICIT_MICROSD_DEVICE bs=4M oflag=direct status=progress
sync
```

The destination must be an explicitly inspected removable microSD device.
Never substitute a guessed device path. Pi 4 and Pi 5 images are deliberately
separate. First boot creates machine identity and SSH host keys; client TLS
credentials are provisioned uniquely and are not embedded in the image.

The Zero W card for release source
`3c5dd917cfe0d6771cdd2e003fa9002dfcb963f2` was flashed with existing device
configuration preserved and passed complete readback, controller identity and
read-only filesystem checks on 2026-09-13. See
[the card verification record](../reports/firmware/zero-w-armhf/performance-card-flash-2026-09-13.json).
Physical board boot and Wii launch qualification remain
`DEFERRED_HARDWARE_UNAVAILABLE`.
