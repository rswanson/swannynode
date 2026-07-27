#!/usr/bin/env bash
# Tests mount_instance_store.sh. The property that matters most: it must select
# devices ONLY by NVMe model string, so an EBS volume can never be formatted.
set -euo pipefail
cd "$(dirname "$0")/.."
pass=0

setup() {
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin" "$tmp/sys" "$tmp/dev" "$tmp/mnt"
  : > "$tmp/fstab"
  : > "$tmp/calls.log"
  for tool in mkfs.ext4 mount mountpoint; do
    printf '#!/usr/bin/env bash\necho "%s $@" >> "%s/calls.log"\n' "$tool" "$tmp" > "$tmp/bin/$tool"
    chmod +x "$tmp/bin/$tool"
  done
  # blkid: rc 2 + no output => provably blank device (the only formattable case)
  printf '#!/usr/bin/env bash\necho "blkid $@" >> "%s/calls.log"\n[ -f "%s/HAS_FS" ] && { echo ext4; exit 0; }\n[ -f "%s/PROBE_ERR" ] && exit 4\nexit 2\n' "$tmp" "$tmp" "$tmp" > "$tmp/bin/blkid"
  chmod +x "$tmp/bin/blkid"
}

# fake_dev <name> <model>
fake_dev() {
  mkdir -p "$tmp/sys/$1/device"
  printf '%s\n' "$2" > "$tmp/sys/$1/device/model"
  touch "$tmp/dev/$1"
}

run() {
  PATH="$tmp/bin:$PATH" SYSBLOCK="$tmp/sys" DEVDIR="$tmp/dev" \
    MOUNT="$tmp/mnt" FSTAB="$tmp/fstab" bash scripts/mount_instance_store.sh 2>&1
}

check() { # check <desc> <condition-result>
  if [ "$2" = "0" ]; then echo "  ok: $1"; pass=$((pass+1));
  else echo "  FAIL: $1" >&2; exit 1; fi
}

echo "case: picks the instance-store device and ignores EBS"
setup
fake_dev nvme0n1 "Amazon Elastic Block Store"          # root volume
fake_dev nvme1n1 "Amazon EC2 NVMe Instance Storage"    # ephemeral
fake_dev nvme2n1 "Amazon Elastic Block Store"          # validator-state volume
run >/dev/null
grep -q "mkfs.ext4 .*$tmp/dev/nvme1n1" "$tmp/calls.log"; check "formats the instance-store device" $?
! grep -q "mkfs.ext4 .*nvme0n1" "$tmp/calls.log"; check "never formats the EBS root volume" $?
! grep -q "mkfs.ext4 .*nvme2n1" "$tmp/calls.log"; check "never formats the EBS validator volume" $?
grep -q "noatime" "$tmp/fstab"; check "fstab entry uses noatime" $?
grep -q "nofail" "$tmp/fstab"; check "fstab entry uses nofail so a missing ephemeral disk cannot wedge boot" $?
grep -q "^LABEL=chain-data $tmp/mnt " "$tmp/fstab"; check "fstab entry mounts by LABEL=chain-data" $?
! grep -q "$tmp/dev/nvme1n1" "$tmp/fstab"; check "fstab entry does not reference the unstable device path" $?
rm -rf "$tmp"

echo "case: existing filesystem is left intact (reboot, not a fresh host)"
setup
fake_dev nvme1n1 "Amazon EC2 NVMe Instance Storage"
touch "$tmp/HAS_FS"
run >/dev/null
! grep -q "mkfs.ext4" "$tmp/calls.log"; check "does not reformat a device that already has a filesystem" $?
rm -rf "$tmp"

echo "case: ambiguous probe refuses to format"
setup
fake_dev nvme1n1 "Amazon EC2 NVMe Instance Storage"
touch "$tmp/PROBE_ERR"
! run >/dev/null 2>&1; check "aborts when blkid probe fails" $?
! grep -q "mkfs.ext4" "$tmp/calls.log"; check "formats nothing on an ambiguous probe" $?
rm -rf "$tmp"

echo "case: no instance store present"
setup
fake_dev nvme0n1 "Amazon Elastic Block Store"
! run >/dev/null 2>&1; check "fails loudly when no instance-store device exists" $?
! grep -q "mkfs.ext4" "$tmp/calls.log"; check "formats nothing when no instance store is found" $?
rm -rf "$tmp"

echo "case: multiple instance-store devices are not silently half-used"
setup
fake_dev nvme1n1 "Amazon EC2 NVMe Instance Storage"
fake_dev nvme2n1 "Amazon EC2 NVMe Instance Storage"
! run >/dev/null 2>&1; check "refuses to guess across multiple devices" $?
! grep -q "mkfs.ext4" "$tmp/calls.log"; check "formats nothing when device count is ambiguous" $?
rm -rf "$tmp"

echo "PASS ($pass assertions)"
