#!/bin/bash

# Environment variables
RETH_DATA_DIR="/data/mainnet/reth"

RUST_LOG=info /data/bin/reth node --instance 1 --datadir $RETH_DATA_DIR --authrpc.jwtsecret /data/shared/jwt.hex --http --http.api all --metrics localhost:9001 --full
