#!/bin/bash
set -euo pipefail

target=${1:?target}
prefix="dist/wiibridge-0.1.0-rc.1-${target}"
image="${prefix}.img"
report="${prefix}.offline-validation.json"
test -s "$image"
partition_text=$(/usr/sbin/sfdisk -d "$image")
printf '%s\n' "$partition_text" | grep -q 'type=c'
root_start=$(printf '%s\n' "$partition_text" | awk -F'[=, ]+' '/img2/{for(i=1;i<=NF;i++)if($i=="start"){print $(i+1);exit}}')
boot_start=$(printf '%s\n' "$partition_text" | awk -F'[=, ]+' '/img1/{for(i=1;i<=NF;i++)if($i=="start"){print $(i+1);exit}}')
test -n "$root_start" -a -n "$boot_start"
tmp=$(mktemp -d)
loop=
cleanup() {
  sudo umount "$tmp/root" "$tmp/boot" 2>/dev/null || true
  test -z "$loop" || sudo losetup -d "$loop" 2>/dev/null || true
  rmdir "$tmp/root" "$tmp/boot" "$tmp" 2>/dev/null || true
}
trap cleanup EXIT
mkdir "$tmp/root" "$tmp/boot"
loop=$(sudo losetup --find --show --partscan --read-only "$image")
sudo /usr/sbin/fsck.vfat -n "${loop}p1"
sudo /usr/sbin/e2fsck -fn "${loop}p2"
sudo mount -o ro "${loop}p2" "$tmp/root"
sudo mount -o ro "${loop}p1" "$tmp/boot"
test -x "$tmp/root/usr/bin/wiibridge-pi-controller"
# Verify the packaged executable is the one produced for this board. This
# catches accidentally packaging an older controller from a cached image.
cmp "build/pi/${target}/wiibridge-pi-controller" \
  "$tmp/root/usr/bin/wiibridge-pi-controller"
test -f "$tmp/root/etc/systemd/system/wiibridge-controller.service"
test -f "$tmp/root/etc/systemd/system/wiibridge-auto-attach.service"
test -L "$tmp/root/etc/systemd/system/multi-user.target.wants/wiibridge-auto-attach.service"
test -f "$tmp/root/etc/systemd/system/wiibridge-setup-ap.service"
test -f "$tmp/root/etc/systemd/system/wiibridge-setup-hostapd.service"
test -f "$tmp/root/etc/systemd/system/wiibridge-setup-dnsmasq.service"
test -f "$tmp/root/usr/libexec/wiibridge-gadget"
test -x "$tmp/root/usr/libexec/wiibridge-setup-ap"
test -x "$tmp/root/usr/libexec/wiibridge-provision"
test -x "$tmp/root/usr/sbin/hostapd"
test -x "$tmp/root/usr/sbin/dnsmasq"
test -f "$tmp/root/etc/wiibridge-dnsmasq.conf"
test -f "$tmp/root/etc/systemd/journald.conf.d/wiibridge.conf"
test -f "$tmp/root/etc/modules-load.d/wiibridge.conf"
test -f "$tmp/root/etc/modprobe.d/wiibridge-nbd.conf"
grep -qx 'nbd' "$tmp/root/etc/modules-load.d/wiibridge.conf"
grep -qx 'options nbd nbds_max=1' \
  "$tmp/root/etc/modprobe.d/wiibridge-nbd.conf"
grep -q '^Storage=persistent$' \
  "$tmp/root/etc/systemd/journald.conf.d/wiibridge.conf"
grep -q '^US$' "$tmp/boot/wiibridge-country"
test "$(cat "$tmp/root/usr/share/wiibridge/board-target")" = "$target"
test ! -s "$tmp/root/etc/machine-id"
test ! -e "$tmp/root/etc/wiibridge/device.key"
if sudo find "$tmp/root" -xdev -type f \( -iname '*.wbfs' -o -iname '*.wbf[0-9]*' \) -print -quit | grep -q .; then
  echo "WBFS payload embedded" >&2
  exit 1
fi
arch=$(file "$tmp/root/usr/bin/wiibridge-pi-controller")
case "$target:$arch" in
  zero-w-armhf:*"32-bit"*"ARM"*) ;;
  pi4-arm64:*"ARM aarch64"*) ;;
  pi5-arm64:*"ARM aarch64"*) ;;
  *) echo "wrong binary architecture: $arch" >&2; exit 1 ;;
esac
case "$target" in
  zero-w-armhf) emulator=qemu-arm-static; export QEMU_CPU=arm1176 ;;
  pi4-arm64|pi5-arm64) emulator=qemu-aarch64-static ;;
esac
smoke=$("$emulator" "$tmp/root/usr/bin/wiibridge-pi-controller" 2>&1 || true)
printf '%s\n' "$smoke" | grep -q 'unique admin token has not been provisioned'
boot_files=$(find "$tmp/boot" -maxdepth 2 -type f -printf '%P\n' | sort)
grep -q '^dtoverlay=dwc2,dr_mode=peripheral$' "$tmp/boot/config.txt"
grep -q 'modules-load=dwc2' "$tmp/boot/cmdline.txt"
case "$target" in
  zero-w-armhf) printf '%s\n' "$boot_files" | grep -Eq 'bcm2708-rpi-zero-w.dtb|bcm2708-rpi-zero.dtb' ;;
  pi4-arm64) printf '%s\n' "$boot_files" | grep -q 'bcm2711-rpi-4-b.dtb' ;;
  pi5-arm64) printf '%s\n' "$boot_files" | grep -q 'bcm2712-rpi-5-b.dtb' ;;
esac
packages="${prefix}.packages.txt"
cp "$tmp/root/var/lib/dpkg/status" "${packages}.status"
awk '/^Package:/{p=$2}/^Version:/{print p"="$2}' "${packages}.status" | sort > "$packages"
rm "${packages}.status"
kernel_versions_text=$(find "$tmp/root/lib/modules" -mindepth 1 -maxdepth 1 -printf '%f\n' | sort)
test -n "$kernel_versions_text"
while IFS= read -r kernel_version; do
  test -n "$kernel_version"
  test -f "$tmp/root/lib/modules/$kernel_version/modules.dep"
  grep -q 'kernel/drivers/block/nbd\.ko' \
    "$tmp/root/lib/modules/$kernel_version/modules.dep"
  find "$tmp/root/lib/modules/$kernel_version" -type f -name 'nbd.ko*' \
    -print -quit | grep -q .
  grep -q 'kernel/drivers/usb/gadget/libcomposite\.ko' \
    "$tmp/root/lib/modules/$kernel_version/modules.dep"
  find "$tmp/root/lib/modules/$kernel_version" -type f -name 'libcomposite.ko*' \
    -print -quit | grep -q .
  grep -q 'kernel/drivers/usb/gadget/function/usb_f_mass_storage\.ko' \
    "$tmp/root/lib/modules/$kernel_version/modules.dep"
  find "$tmp/root/lib/modules/$kernel_version" -type f -name 'usb_f_mass_storage.ko*' \
    -print -quit | grep -q .
done <<< "$kernel_versions_text"
kernel_versions=$(printf '%s\n' "$kernel_versions_text" | jq -Rsc 'split("\n")[:-1]')
sudo umount "$tmp/root" "$tmp/boot"
sudo losetup -d "$loop"
loop=
jq -n --arg target "$target" --arg architecture "$arch" \
  --argjson kernel_versions "$kernel_versions" \
  '{status:"firmware-offline-verified",target:$target,architecture:$architecture,
    partition_table:"PASS",filesystems_non_destructive_fsck:"PASS",
    filesystems_read_only_inspection:"PASS",
    board_metadata:"PASS",boot_files:"PASS",usb_gadget_boot_config:"PASS",
    services:"PASS",
    nbd_kernel_modules:"PASS",nbd_boot_preload:"PASS",
    usb_gadget_kernel_modules:"PASS",
    qemu_application_smoke:"PASS",controller_matches_build:"PASS",
    no_payloads:"PASS",no_embedded_identity:"PASS",
    kernel_versions:$kernel_versions,
    physical_tests:"DEFERRED_HARDWARE_UNAVAILABLE"}' > "$report"
