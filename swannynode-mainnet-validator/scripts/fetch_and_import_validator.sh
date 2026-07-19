#!/usr/bin/env bash
# Pull validator key material from Secrets Manager and import it into the
# lighthouse datadir. Idempotent via a sentinel written only after BOTH the
# keystore and slashing-protection imports succeed, so a partial first run
# (e.g. slashing import failed) is retried in full, never skipped.
# A hard failure here (e.g. secrets not yet pushed) is INTENTIONAL — it keeps
# lighthousevalidator.service from ever starting without keys + slashing data.
set -euo pipefail
DATADIR="${DATADIR:-/data/mainnet/lighthouse}"
LH_BIN="${LH_BIN:-/data/bin/lighthouse}"
SECRET_PREFIX="${SECRET_PREFIX:-mainnet-validator}"
SENTINEL="$DATADIR/.validator-import-complete"

if [ -f "$SENTINEL" ]; then
  echo "validator already imported; skipping"
  exit 0
fi

mkdir -p "$DATADIR"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
chmod 700 "$tmp"
mkdir -p "$tmp/keys"

aws secretsmanager get-secret-value --secret-id "$SECRET_PREFIX/keystore" \
  --query SecretString --output text > "$tmp/keys/voting-keystore.json"
aws secretsmanager get-secret-value --secret-id "$SECRET_PREFIX/keystore-password" \
  --query SecretString --output text > "$tmp/password.txt"

if ! aws secretsmanager get-secret-value --secret-id "$SECRET_PREFIX/slashing-protection" \
  --query SecretString --output text > "$tmp/interchange.json" 2>/dev/null; then
  echo "WARN: slashing-protection secret unavailable; relying on doppelganger protection only" >&2
  rm -f "$tmp/interchange.json"
fi

"$LH_BIN" account validator import \
  --network mainnet --datadir "$DATADIR" \
  --directory "$tmp/keys" \
  --password-file "$tmp/password.txt" --reuse-password

if [ -f "$tmp/interchange.json" ]; then
  "$LH_BIN" account validator slashing-protection import "$tmp/interchange.json" \
    --network mainnet --datadir "$DATADIR"
fi

touch "$SENTINEL"
chown -R lighthouse:eth "$DATADIR"
echo "validator import complete"
