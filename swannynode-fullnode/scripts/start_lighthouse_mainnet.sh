#!/bin/bash

# Environment variables
NETWORK="mainnet"
DATA_DIR="/data/${NETWORK}/lighthouse"
# Start lighthouse
/data/bin/lighthouse bn \
  --network $NETWORK \
  --datadir $DATA_DIR \
  --http \
  --http-port 6052 \
  --metrics \
  --metrics-port 6064 \
  --disable-deposit-contract-sync \
  --checkpoint-sync-url https://mainnet.checkpoint.sigp.io \
  --execution-endpoint http://127.0.0.1:8551 \
  --execution-jwt /data/shared/jwt.hex \
  --port 10000