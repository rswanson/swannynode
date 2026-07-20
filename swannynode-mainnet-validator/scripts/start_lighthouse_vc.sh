#!/usr/bin/env bash
set -euo pipefail
: "${FEE_RECIPIENT:?FEE_RECIPIENT must be set via /etc/swannynode/validator.env}"
exec /data/bin/lighthouse vc \
  --network mainnet \
  --datadir /data/mainnet/lighthouse \
  --beacon-nodes http://127.0.0.1:5052 \
  --suggested-fee-recipient "$FEE_RECIPIENT" \
  --builder-proposals \
  --enable-doppelganger-protection \
  --metrics --metrics-address 0.0.0.0 --metrics-port 6065
