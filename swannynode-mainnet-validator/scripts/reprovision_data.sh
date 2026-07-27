#!/usr/bin/env bash
# Boot-time self-heal for ephemeral instance-store /data.
#
# Instance-store contents are LOST on stop/start (and on instance
# replacement) but the root volume is not. This script is run by
# data-reprovision.service, ordered before reth-init.service, on every boot.
# When it finds /data empty or unmounted it re-mounts the instance-store NVMe,
# rebuilds the directory layout, and re-downloads client binaries, so a bare
# stop/start recovers unattended instead of coming up with a blank /data and
# every unit either failing or silently doing nothing.
#
# Idempotent and a fast no-op when /data is already populated (the common
# case: every normal reboot, and immediately after this same bootstrap runs
# it once itself). On EBS-backed stacks (USE_INSTANCE_STORE=false) this is
# always a no-op: /data there is durable EBS mounted via fstab, not our
# concern here.
set -euo pipefail

ENV_FILE="${ENV_FILE:-/etc/swannynode/storage.env}"
SCRIPTS_DIR="${SCRIPTS_DIR:-/opt/swannynode/scripts}"
MOUNT="${MOUNT:-/data}"

# shellcheck disable=SC1090
[ -f "$ENV_FILE" ] && . "$ENV_FILE"

if [ "${USE_INSTANCE_STORE:-false}" != "true" ]; then
  echo "EBS-backed /data; nothing to reprovision"
  exit 0
fi

# Fast path: already mounted with a populated layout -> no-op.
if mountpoint -q "$MOUNT" && [ -x "$MOUNT/bin/reth" ]; then
  echo "$MOUNT already provisioned; nothing to do"
  exit 0
fi

echo "$MOUNT missing or unpopulated; reprovisioning instance-store"

MOUNT="$MOUNT" "$SCRIPTS_DIR/mount_instance_store.sh"

mkdir -p "$MOUNT/bin" "$MOUNT/shared" "$MOUNT/mainnet/reth" "$MOUNT/mainnet/lighthouse"

if [ ! -f "$MOUNT/shared/jwt.hex" ]; then
  (umask 077 && openssl rand -hex 32 > "$MOUNT/shared/jwt.hex")
fi
chgrp eth "$MOUNT/shared/jwt.hex"
chmod 640 "$MOUNT/shared/jwt.hex"

RETH_VERSION="${RETH_VERSION:?}" \
LIGHTHOUSE_VERSION="${LIGHTHOUSE_VERSION:?}" \
MEVBOOST_VERSION="${MEVBOOST_VERSION:?}" \
BIN="$MOUNT/bin" \
  "$SCRIPTS_DIR/install_clients.sh"

chown -R reth:eth "$MOUNT/mainnet/reth"
chown -R lighthouse:eth "$MOUNT/mainnet/lighthouse"
chown root:eth "$MOUNT/bin" "$MOUNT/shared"

echo "$MOUNT reprovisioned"
