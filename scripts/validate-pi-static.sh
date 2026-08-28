#!/bin/bash
set -euo pipefail

shellcheck \
  pi/packaging/pi-gen/common/files/wiibridge-helper \
  pi/packaging/pi-gen/common/files/wiibridge-gadget \
  pi/packaging/pi-gen/common/files/wiibridge-firstboot \
  pi/packaging/pi-gen/common/files/wiibridge-setup-ap \
  pi/packaging/pi-gen/common/files/wiibridge-provision \
  scripts/repair-live-card.sh
verify_root=build/systemd-verify-root
mkdir -p "$verify_root/etc/systemd/system" "$verify_root/usr/lib/systemd/system" \
  "$verify_root/usr/libexec" "$verify_root/usr/bin" "$verify_root/usr/sbin"
cp -a /usr/lib/systemd/system/. "$verify_root/usr/lib/systemd/system/"
cp pi/packaging/systemd/*.service "$verify_root/etc/systemd/system/"
cp pi/packaging/pi-gen/common/files/wiibridge-firstboot \
  pi/packaging/pi-gen/common/files/wiibridge-helper \
  pi/packaging/pi-gen/common/files/wiibridge-setup-ap \
  pi/packaging/pi-gen/common/files/wiibridge-provision \
  "$verify_root/usr/libexec/"
cp build/bin/wiibridge-host "$verify_root/usr/bin/wiibridge-pi-controller"
for command in hostapd dnsmasq; do
  printf '#!/bin/sh\nexit 0\n' > "$verify_root/usr/sbin/$command"
  chmod 0755 "$verify_root/usr/sbin/$command"
done
systemd-analyze verify --root="$verify_root" \
  wiibridge-firstboot.service wiibridge-controller.service \
  wiibridge-auto-attach.service \
  wiibridge-recover.service wiibridge-setup-ap.service \
  wiibridge-setup-hostapd.service wiibridge-setup-dnsmasq.service
for target in zero-w-armhf pi4-arm64 pi5-arm64; do
  test "$(grep -c "^IMG_NAME=.*${target}$" "pi/packaging/pi-gen/${target}/config")" = 1
done
grep -q 'lun.0/ro' pi/packaging/pi-gen/common/files/wiibridge-gadget
grep -q 'blockdev --getro' pi/packaging/pi-gen/common/files/wiibridge-gadget
grep -q 'gamecube-emulated:0' pi/packaging/pi-gen/common/files/wiibridge-gadget
grep -q 'connect-gamecube-physical' pi/packaging/pi-gen/common/files/wiibridge-helper
grep -q 'connect-gamecube-emulated' pi/packaging/pi-gen/common/files/wiibridge-helper
grep -q 'blockdev --setrw' pi/packaging/pi-gen/common/files/wiibridge-helper
for action in connect connect-wii connect-gamecube-physical \
  connect-gamecube-emulated disconnect attach detach clear-cache test \
  poweroff reboot; do
  grep -Fqx \
    "wiibridge ALL=(root) NOPASSWD: /usr/libexec/wiibridge-helper $action" \
    pi/packaging/pi-gen/common/files/wiibridge-sudoers
done
grep -q 'nbd-client -x' pi/packaging/pi-gen/common/files/wiibridge-helper
if grep -q 'modprobe nbd' pi/packaging/pi-gen/common/files/wiibridge-helper; then
  echo "the sandboxed helper must not load kernel modules" >&2
  exit 1
fi
grep -qx 'nbd' \
  pi/packaging/pi-gen/common/files/wiibridge-modules-load.conf
grep -qx 'libcomposite' \
  pi/packaging/pi-gen/common/files/wiibridge-modules-load.conf
grep -qx 'usb_f_mass_storage' \
  pi/packaging/pi-gen/common/files/wiibridge-modules-load.conf
grep -qx 'options nbd nbds_max=1' \
  pi/packaging/pi-gen/common/files/wiibridge-modprobe.conf
grep -q 'ProtectKernelModules=yes' \
  pi/packaging/systemd/wiibridge-controller.service
grep -q 'After=systemd-modules-load.service' \
  pi/packaging/systemd/wiibridge-controller.service
grep -q 'auto-attach)' pi/packaging/pi-gen/common/files/wiibridge-helper
grep -q 'auto-attach-ready)' pi/packaging/pi-gen/common/files/wiibridge-helper
grep -q 'export WIIBRIDGE_USB_VID WIIBRIDGE_USB_PID' \
  pi/packaging/pi-gen/common/files/wiibridge-helper
grep -q 'ExecCondition=/usr/libexec/wiibridge-helper auto-attach-ready' \
  pi/packaging/systemd/wiibridge-auto-attach.service
grep -q 'test -b /dev/nbd0p1' \
  pi/packaging/pi-gen/common/files/wiibridge-helper
grep -q 'if=/dev/nbd0p1' \
  pi/packaging/pi-gen/common/files/wiibridge-helper
grep -q 'poweroff)' pi/packaging/pi-gen/common/files/wiibridge-helper
grep -q 'systemctl poweroff --no-block' \
  pi/packaging/pi-gen/common/files/wiibridge-helper
grep -q 'wiibridge-helper poweroff' \
  pi/packaging/pi-gen/common/files/wiibridge-sudoers
grep -q 'ConditionPathExists=/etc/wiibridge/auto-attach' \
  pi/packaging/systemd/wiibridge-auto-attach.service
grep -q 'exit 0' pi/packaging/pi-gen/common/files/wiibridge-helper
grep -q 'wiibridge-setup' pi/packaging/pi-gen/common/files/wiibridge-firstboot
grep -Fq -- '-days 36525' \
  pi/packaging/pi-gen/common/files/wiibridge-firstboot
if grep -Fq -- '-days 30 ' \
  pi/packaging/pi-gen/common/files/wiibridge-firstboot; then
  echo "short-lived Pi device certificate generation is forbidden" >&2
  exit 1
fi
grep -Fq "admin_token=\$(openssl rand -hex 6)" \
  pi/packaging/pi-gen/common/files/wiibridge-firstboot
grep -q '10.77.0.1/24' pi/packaging/pi-gen/common/files/wiibridge-setup-ap
grep -q 'wpa_key_mgmt=WPA-PSK' \
  pi/packaging/pi-gen/common/files/wiibridge-firstboot
grep -q 'systemctl start wiibridge-setup-hostapd.service' \
  pi/packaging/pi-gen/common/files/wiibridge-setup-ap
grep -q 'dhcp-range=10.77.0.20,10.77.0.100' \
  pi/packaging/pi-gen/common/files/wiibridge-dnsmasq.conf
grep -q 'dhcp-leasefile=/run/wiibridge-dnsmasq/dnsmasq.leases' \
  pi/packaging/pi-gen/common/files/wiibridge-dnsmasq.conf
grep -q '^User=dnsmasq$' \
  pi/packaging/systemd/wiibridge-setup-dnsmasq.service
grep -q 'conf-file=/etc/wiibridge-dnsmasq.conf' \
  pi/packaging/systemd/wiibridge-setup-dnsmasq.service
grep -q 'wiibridge-client' pi/packaging/pi-gen/common/files/wiibridge-provision
grep -q 'wifi-update' pi/packaging/pi-gen/common/files/wiibridge-provision
grep -q 'verify -purpose sslclient' \
  pi/packaging/pi-gen/common/files/wiibridge-provision
grep -Fq \
  'ReadWritePaths=/run/wiibridge /etc/NetworkManager/system-connections /etc/wiibridge /boot/firmware' \
  pi/packaging/systemd/wiibridge-controller.service
grep -q 'Storage=persistent' \
  pi/packaging/pi-gen/common/files/wiibridge-journald.conf
if grep -R -E --include='*' \
  'BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY|password[=:][^ ]+|psk[=:][^ ]+' \
  pi deploy server config 2>/dev/null; then
  echo "private credential material found in source" >&2
  exit 1
fi
if grep -R -Ei --include='*' \
  'usb hat|ethernet hat|hub hat|hub-detection|dtoverlay=.*hub' \
  pi deploy server docs/gamecube-support.md diagnostics/gamecube 2>/dev/null; then
  echo "unsupported HAT or hub support found" >&2
  exit 1
fi
