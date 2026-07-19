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

# Three-way guard: format ONLY a provably blank device. blkid rc=2 means
# "probed fine, no filesystem found"; any other failure (permissions,
# transient error) must abort rather than risk formatting real data.
set +e
FS_TYPE=$(blkid -o value -s TYPE "$DEV" 2>/dev/null)
probe_rc=$?
set -e
if [ -n "$FS_TYPE" ]; then
  echo "existing $FS_TYPE filesystem found; leaving data intact"
elif [ "$probe_rc" -eq 2 ]; then
  echo "blank device; creating ext4 filesystem"
  mkfs.ext4 -L validator-data "$DEV"
else
  echo "blkid probe failed (rc=$probe_rc); refusing to format" >&2
  exit 1
fi

UUID=$(blkid -s UUID -o value "$DEV")
if ! grep -q "UUID=$UUID" "$FSTAB"; then
  echo "UUID=$UUID $MOUNT ext4 defaults,nofail 0 2" >> "$FSTAB"
fi
mkdir -p "$MOUNT"
mount -a
mountpoint -q "$MOUNT" || { echo "$MOUNT is not mounted" >&2; exit 1; }
echo "data volume mounted at $MOUNT"
