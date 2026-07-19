#!/usr/bin/env bash
set -euo pipefail
exec /data/bin/lighthouse bn \
  --network mainnet \
  --datadir /data/mainnet/lighthouse \
  --execution-endpoint http://127.0.0.1:8551 \
  --execution-jwt /data/shared/jwt.hex \
  --checkpoint-sync-url https://mainnet.checkpoint.sigp.io \
  --builder http://127.0.0.1:18550 \
  --http --http-address 127.0.0.1 --http-port 5052 \
  --metrics --metrics-address 0.0.0.0 --metrics-port 6064 \
  --port 9000 --quic-port 9001
