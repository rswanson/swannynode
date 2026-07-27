#!/usr/bin/env bash
# Format-if-blank and mount the ephemeral instance-store NVMe at /data.
#
# Instance-store contents are LOST on stop/start and on host replacement, but
# survive a reboot — so format-if-blank is the correct policy here, unlike the
# EBS path where a blank device means a genuinely new volume.
#
# SAFETY: this script formats. It selects devices SOLELY by NVMe model string
# ("Amazon EC2 NVMe Instance Storage"), so it can never touch an EBS volume —
# EBS reports "Amazon Elastic Block Store". Nothing here derives a device from
# a size or an ordinal, both of which can shift between boots.
set -euo pipefail
MOUNT="${MOUNT:-/data}"
FSTAB="${FSTAB:-/etc/fstab}"
SYSBLOCK="${SYSBLOCK:-/sys/block}"
DEVDIR="${DEVDIR:-/dev}"
INSTANCE_STORE_MODEL="Amazon EC2 NVMe Instance Storage"

devices=()
for sysdev in "$SYSBLOCK"/nvme*; do
  [ -e "$sysdev/device/model" ] || continue
  model=$(tr -d '\n' < "$sysdev/device/model" | sed 's/[[:space:]]*$//')
  if [ "$model" = "$INSTANCE_STORE_MODEL" ]; then
    devices+=("$DEVDIR/$(basename "$sysdev")")
  fi
done

if [ "${#devices[@]}" -eq 0 ]; then
  echo "no instance-store NVMe found (looked for model '$INSTANCE_STORE_MODEL')" >&2
  echo "is this instance type actually storage-backed?" >&2
  exit 1
fi
if [ "${#devices[@]}" -gt 1 ]; then
  # Deliberately not guessing. Striping N devices needs mdadm and changes the
  # failure model; silently using one would waste most of the capacity.
  echo "found ${#devices[@]} instance-store devices: ${devices[*]}" >&2
  echo "this script handles exactly one; add a RAID0 step for multi-device types" >&2
  exit 1
fi

DEV="${devices[0]}"
echo "instance-store device: $DEV"

# Three-way guard, matching mount_data.sh: format ONLY a provably blank device.
# blkid rc=2 means "probed fine, no filesystem"; any other failure must abort.
set +e
FS_TYPE=$(blkid -o value -s TYPE "$DEV" 2>/dev/null)
probe_rc=$?
set -e
if [ -n "$FS_TYPE" ]; then
  echo "existing $FS_TYPE filesystem found; leaving intact (reboot, not a fresh host)"
elif [ "$probe_rc" -eq 2 ]; then
  echo "blank device; creating ext4 filesystem"
  # No lazy init: we want the cost paid now, not bleeding into block validation.
  mkfs.ext4 -L chain-data -E lazy_itable_init=0,lazy_journal_init=0 "$DEV"
else
  echo "blkid probe failed (rc=$probe_rc); refusing to format" >&2
  exit 1
fi

mkdir -p "$MOUNT"
# nofail so a missing ephemeral device can never wedge boot; noatime because
# reth's read volume makes atime updates pure write amplification.
# Mounted by device path, not UUID: mkfs runs again on every fresh host, so a
# UUID baked into fstab would go stale exactly when it matters.
if ! grep -q "[[:space:]]$MOUNT[[:space:]]" "$FSTAB"; then
  echo "$DEV $MOUNT ext4 defaults,nofail,noatime 0 2" >> "$FSTAB"
fi
mountpoint -q "$MOUNT" || mount "$MOUNT"
mountpoint -q "$MOUNT" || { echo "$MOUNT is not mounted" >&2; exit 1; }
echo "instance-store volume mounted at $MOUNT"
