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
# blkid behavior: TYPE probe prints ext4 (rc 0) when BLKID_HAS_FS exists,
# rc 2 + no output for a blank device, rc 4 when BLKID_ERROR simulates a
# probe failure; UUID probe prints a fixed UUID.
cat > "$tmp/bin/blkid" <<EOF
#!/usr/bin/env bash
echo "blkid \$@" >> "$tmp/calls.log"
[ -f "$tmp/BLKID_ERROR" ] && exit 4
if echo "\$@" | grep -q "UUID"; then echo "test-uuid-1234"; exit 0; fi
if echo "\$@" | grep -q "TYPE"; then
  if [ -f "$tmp/BLKID_HAS_FS" ]; then echo "ext4"; exit 0; else exit 2; fi
fi
exit 0
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

# Case 3 (regression): blkid probe error -> abort, never mkfs.
rm -f "$tmp/BLKID_HAS_FS"; touch "$tmp/BLKID_ERROR"; echo -n > "$tmp/calls.log"
if PATH="$tmp/bin:$PATH" VOLUME_ID=vol-abc123 DEV_OVERRIDE="$tmp/dev-node" FSTAB="$tmp/fstab" MOUNT="$tmp/mnt" bash scripts/mount_data.sh 2>/dev/null; then
  echo "FAIL: expected abort when blkid probe errors"; exit 1
fi
grep -q "mkfs.ext4" "$tmp/calls.log" && { echo "FAIL: mkfs must not run when probe fails"; exit 1; }
rm -f "$tmp/BLKID_ERROR"

echo "PASS test_mount_data"
