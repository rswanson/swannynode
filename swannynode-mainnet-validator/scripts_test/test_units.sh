#!/usr/bin/env bash
# Invariants that make the unit graph safe:
# 1. Every unit that touches /data declares RequiresMountsFor=/data
#    (a failed mount must stop clients from initializing on the root disk).
# 2. reth requires reth-init; the vc requires validator-init.
# 3. Long-running services restart automatically.
set -euo pipefail
cd "$(dirname "$0")/../config"

for unit in reth-init.service reth.service lighthousebeacon.service validator-init.service lighthousevalidator.service mevboost.service; do
  [ -f "$unit" ] || { echo "FAIL: missing $unit"; exit 1; }
  grep -q "RequiresMountsFor=/data" "$unit" || { echo "FAIL: $unit lacks RequiresMountsFor=/data"; exit 1; }
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

echo "PASS test_units"
