#!/usr/bin/env bash
# Tests mount_data.sh: formats only blank devices, appends fstab once.
set -euo pipefail
cd "$(dirname "$0")/.."
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"
touch "$tmp/dev-node" "$tmp/fstab"
echo -n > "$tmp/calls.log"

for tool in blkid mkfs.ext4 mount mountpoint; do
  cat > "$tmp/bin/$tool" <<EOF
#!/usr/bin/env bash
echo "$tool \$@" >> "$tmp/calls.log"
EOF
  chmod +x "$tmp/bin/$tool"
done
# blkid behavior: fail (blank device) unless BLKID_HAS_FS marker exists; -s UUID prints a fixed UUID.
cat > "$tmp/bin/blkid" <<EOF
#!/usr/bin/env bash
echo "blkid \$@" >> "$tmp/calls.log"
if [ "\$1" = "-s" ]; then echo "test-uuid-1234"; exit 0; fi
[ -f "$tmp/BLKID_HAS_FS" ] || exit 2
EOF
chmod +x "$tmp/bin/blkid"

# Case 1: blank device -> mkfs runs, fstab gains one line.
PATH="$tmp/bin:$PATH" VOLUME_ID=vol-abc123 DEV_OVERRIDE="$tmp/dev-node" FSTAB="$tmp/fstab" MOUNT="$tmp/mnt" bash scripts/mount_data.sh
grep -q "mkfs.ext4" "$tmp/calls.log" || { echo "FAIL: expected mkfs on blank device"; exit 1; }
[ "$(grep -c "test-uuid-1234" "$tmp/fstab")" = "1" ] || { echo "FAIL: expected one fstab entry"; exit 1; }

# Case 2: device already has a filesystem -> no mkfs, fstab unchanged (idempotent re-run).
touch "$tmp/BLKID_HAS_FS"; echo -n > "$tmp/calls.log"
PATH="$tmp/bin:$PATH" VOLUME_ID=vol-abc123 DEV_OVERRIDE="$tmp/dev-node" FSTAB="$tmp/fstab" MOUNT="$tmp/mnt" bash scripts/mount_data.sh
grep -q "mkfs.ext4" "$tmp/calls.log" && { echo "FAIL: mkfs must not run on formatted device"; exit 1; }
[ "$(grep -c "test-uuid-1234" "$tmp/fstab")" = "1" ] || { echo "FAIL: fstab entry duplicated"; exit 1; }

echo "PASS test_mount_data"
