#!/bin/sh
# Prepare a macOS template (after `magi-vz install`) for task VMs. Run it
# as root on the Mac, with the VM stopped:
#   prepare-template.sh DIR
# It mounts the Data volume of DIR/disk.img and adds a launchd daemon that
# starts envd from the share "magi" at boot. It also marks Setup Assistant
# as done. It changes nothing else in the guest.
set -eu
dir=${1:?DIR}
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
disk=$(diskutil image attach --noMount "$dir/disk.img" | awk 'NR==1{print $1}')
trap 'hdiutil detach "$disk" >/dev/null 2>&1 || hdiutil detach -force "$disk" >/dev/null 2>&1 || true' EXIT
sleep 2
# The Data volume of the containers in this image only.
data=
for c in $(diskutil list "$disk" | awk '/Apple_APFS /{for (i = 1; i < NF; i++) if ($i == "Container") print $(i + 1)}'); do
  v=$(diskutil apfs list "$c" | awk '/APFS Volume Disk \(Role\):.*\(Data\)/{print $(NF-1)}' | head -1)
  [ -n "$v" ] && data=$v
done
[ -n "$data" ] || { echo "no Data volume in $dir/disk.img" >&2; exit 1; }
mnt=$(cd "$(mktemp -d)" && pwd -P)
diskutil mount -mountPoint "$mnt" "$data" >/dev/null
mount | grep -q " on $mnt " || { echo "could not mount $data at $mnt" >&2; rmdir "$mnt"; exit 1; }
# An attached image mounts with ownership off; launchd needs root:wheel.
diskutil enableOwnership "$mnt" >/dev/null
mkdir -p "$mnt/usr/local/magi" "$mnt/Library/LaunchDaemons" "$mnt/private/var/db"
cat > "$mnt/usr/local/magi/boot.sh" <<'BOOT'
#!/bin/sh
# Starts envd of a Magi task VM from the share "magi" (magi-vz).
S="/Volumes/My Shared Files"
i=0
while [ ! -x "$S/magi/envd" ]; do
  i=$((i + 1)); [ $i -gt 600 ] && exit 1
  sleep 1
done
exec >>"$S/state/guest.log" 2>&1
# The work directory is on the guest disk: files on a share have no owner
# (Git refuses such repositories), and the guest disk is faster.
[ -L /Users/Shared/work ] && rm /Users/Shared/work
mkdir -p /Users/Shared/work
exec "$S/magi/envd" envd -vm
BOOT
cat > "$mnt/Library/LaunchDaemons/community.catalyst.magi.envd.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>community.catalyst.magi.envd</string>
	<key>ProgramArguments</key><array><string>/usr/local/magi/boot.sh</string></array>
	<key>RunAtLoad</key><true/>
	<key>StandardOutPath</key><string>/var/log/magi-envd.log</string>
	<key>StandardErrorPath</key><string>/var/log/magi-envd.log</string>
</dict>
</plist>
PLIST
chown -R root:wheel "$mnt/usr/local/magi" "$mnt/Library/LaunchDaemons/community.catalyst.magi.envd.plist"
chmod 755 "$mnt/usr/local/magi" "$mnt/usr/local/magi/boot.sh"
chmod 644 "$mnt/Library/LaunchDaemons/community.catalyst.magi.envd.plist"
touch "$mnt/private/var/db/.AppleSetupDone"
chown root:wheel "$mnt/private/var/db/.AppleSetupDone"
diskutil unmount "$mnt" >/dev/null
rmdir "$mnt"
echo "prepared $dir"
