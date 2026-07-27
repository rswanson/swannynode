#!/usr/bin/env bash
set -euo pipefail
: "${FEE_RECIPIENT:?FEE_RECIPIENT must be set via /etc/swannynode/validator.env}"
# VALIDATOR_DATADIR holds the keystores and the slashing-protection DB. It is
# deliberately NOT under /data: on instance-store hosts /data is ephemeral, and
# losing the slashing DB is the one failure that can get you slashed.
VALIDATOR_DATADIR="${VALIDATOR_DATADIR:-/validator/lighthouse}"

# Doppelganger costs ~2 epochs (~13 min) of deliberate silence on every start.
# That is the right default. It is switched off for exactly one start during a
# scripted cutover, where the old signer has already been proven dead — see
# cutover_validator.sh.
DOPPELGANGER_ARGS=(--enable-doppelganger-protection)
if [ "${DOPPELGANGER:-on}" = "off" ]; then
  echo "WARNING: doppelganger protection DISABLED for this start" >&2
  DOPPELGANGER_ARGS=()
fi

exec /data/bin/lighthouse vc \
  --network mainnet \
  --datadir "$VALIDATOR_DATADIR" \
  --beacon-nodes http://127.0.0.1:5052 \
  --suggested-fee-recipient "$FEE_RECIPIENT" \
  --builder-proposals \
  "${DOPPELGANGER_ARGS[@]}" \
  --metrics --metrics-address 0.0.0.0 --metrics-port 6065
