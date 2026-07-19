#!/usr/bin/env bash
# One-time reth snapshot initialization. Idempotent: a datadir that already
# contains a database is NEVER touched — this is the guard that makes every
# recovery path (fresh volume, reattached volume) safe to converge through.
set -euo pipefail
DATADIR="${DATADIR:-/data/mainnet/reth}"
RETH_BIN="${RETH_BIN:-/data/bin/reth}"
MAX_ATTEMPTS="${MAX_ATTEMPTS:-10}"
RETRY_DELAY="${RETRY_DELAY:-30}"

if [ -f "$DATADIR/db/mdbx.dat" ]; then
  echo "reth datadir already initialized; skipping snapshot download"
  exit 0
fi

mkdir -p "$DATADIR"
attempt=0
# reth download supports HTTP-Range resume, so re-invoking after an
# interruption continues the download rather than restarting it.
until "$RETH_BIN" download --minimal -y --chain mainnet --datadir "$DATADIR"; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge "$MAX_ATTEMPTS" ]; then
    echo "snapshot download failed after $MAX_ATTEMPTS attempts" >&2
    exit 1
  fi
  echo "download interrupted; resuming in ${RETRY_DELAY}s (attempt $attempt/$MAX_ATTEMPTS)"
  sleep "$RETRY_DELAY"
done
echo "snapshot download complete"
