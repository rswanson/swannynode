#!/usr/bin/env bash
# Format-if-blank and mount the EBS data volume at /data.
# Never formats a device that already has a filesystem (a reattached volume).
set -euo pipefail
VOLUME_ID="${VOLUME_ID:?VOLUME_ID (vol-...) is required}"
MOUNT="${MOUNT:-/data}"
FSTAB="${FSTAB:-/etc/fstab}"
# On Nitro, EBS volumes surface as NVMe with the volume id (sans dash) as serial.
DEV="${DEV_OVERRIDE:-/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_${VOLUME_ID/-/}}"

for _ in $(seq 1 60); do
  [ -e "$DEV" ] && break
  sleep 2
done
[ -e "$DEV" ] || { echo "device $DEV not found after 120s" >&2; exit 1; }

if ! blkid "$DEV" >/dev/null 2>&1; then
  echo "blank device; creating ext4 filesystem"
  mkfs.ext4 -L validator-data "$DEV"
fi

UUID=$(blkid -s UUID -o value "$DEV")
if ! grep -q "UUID=$UUID" "$FSTAB"; then
  echo "UUID=$UUID $MOUNT ext4 defaults,nofail 0 2" >> "$FSTAB"
fi
mkdir -p "$MOUNT"
mount -a
mountpoint -q "$MOUNT" || { echo "$MOUNT is not mounted" >&2; exit 1; }
echo "data volume mounted at $MOUNT"
