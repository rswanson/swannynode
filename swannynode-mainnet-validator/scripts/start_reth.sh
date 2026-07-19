#!/usr/bin/env bash
set -euo pipefail
exec /data/bin/reth node \
  --minimal \
  --chain mainnet \
  --datadir /data/mainnet/reth \
  --authrpc.addr 127.0.0.1 \
  --authrpc.port 8551 \
  --authrpc.jwtsecret /data/shared/jwt.hex \
  --metrics 0.0.0.0:9001 \
  --port 30303
