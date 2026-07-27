#!/usr/bin/env bash
# Invariants that make the unit graph safe:
# 1. Every unit that touches /data declares RequiresMountsFor=/data as an
#    EXACT line in the base unit file. Base units must stay mode-agnostic —
#    the instance-store /validator condition is added only via a drop-in
#    written by deploy.go, never fused into this line (e.g.
#    "RequiresMountsFor=/data /validator" would permanently block EBS-backed
#    stacks, which never have /validator at all). A substring match would
#    pass either way, so this must be a whole-line match.
# 2. reth requires reth-init; the vc requires validator-init.
# 3. Long-running services restart automatically.
# 4. Every unit's ExecStart runs from /opt/swannynode/scripts on the root
#    volume, not the old /data/scripts location — /data can be wiped by an
#    instance-store stop/start, which would take the units' own executables
#    with it.
# 5. data-reprovision.service self-heals /data on every boot and must be
#    ordered before reth-init.service, the first unit that actually needs
#    /data populated.
set -euo pipefail
cd "$(dirname "$0")/../config"

BASE_UNITS=(reth-init.service reth.service lighthousebeacon.service validator-init.service lighthousevalidator.service mevboost.service)

for unit in "${BASE_UNITS[@]}"; do
  [ -f "$unit" ] || { echo "FAIL: missing $unit"; exit 1; }
  grep -qx "RequiresMountsFor=/data" "$unit" || { echo "FAIL: $unit must contain the exact line 'RequiresMountsFor=/data' (mode-agnostic base units must not fuse /validator into this line)"; exit 1; }
done

grep -q "Requires=reth-init.service" reth.service || { echo "FAIL: reth.service must require reth-init"; exit 1; }
grep -q "After=reth-init.service" reth.service || { echo "FAIL: reth.service must order after reth-init"; exit 1; }
grep -q "Requires=validator-init.service" lighthousevalidator.service || { echo "FAIL: vc must require validator-init"; exit 1; }
grep -q "After=validator-init.service" lighthousevalidator.service || { echo "FAIL: vc must order after validator-init"; exit 1; }

for unit in reth.service lighthousebeacon.service lighthousevalidator.service mevboost.service; do
  grep -q "Restart=always" "$unit" || { echo "FAIL: $unit lacks Restart=always"; exit 1; }
done

for unit in reth-init.service validator-init.service; do
  grep -q "Type=oneshot" "$unit" || { echo "FAIL: $unit must be Type=oneshot"; exit 1; }
done

# --- data-reprovision.service: must exist, be a oneshot, and run before
# reth-init.service.
[ -f "data-reprovision.service" ] || { echo "FAIL: missing data-reprovision.service"; exit 1; }
grep -q "Type=oneshot" data-reprovision.service || { echo "FAIL: data-reprovision.service must be Type=oneshot"; exit 1; }
grep -q "Before=reth-init.service" data-reprovision.service || { echo "FAIL: data-reprovision.service must be ordered before reth-init.service"; exit 1; }

# --- every unit's ExecStart must run from /opt/swannynode/scripts, including
# data-reprovision.service itself.
for unit in "${BASE_UNITS[@]}" data-reprovision.service; do
  grep -q "^ExecStart=/opt/swannynode/scripts/" "$unit" || { echo "FAIL: $unit's ExecStart must run from /opt/swannynode/scripts"; exit 1; }
done

echo "PASS test_units"
