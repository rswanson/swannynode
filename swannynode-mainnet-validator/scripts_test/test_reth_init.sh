#!/usr/bin/env bash
# Tests reth_init.sh: downloads only when the datadir is empty.
set -euo pipefail
cd "$(dirname "$0")/.."
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

# Stub reth binary that records its invocation.
cat > "$tmp/reth" <<'EOF'
#!/usr/bin/env bash
echo "$@" >> "$(dirname "$0")/reth_calls.log"
exit 0
EOF
chmod +x "$tmp/reth"

# Case 1: empty datadir -> download runs with --minimal.
DATADIR="$tmp/empty" RETH_BIN="$tmp/reth" RETRY_DELAY=0 bash scripts/reth_init.sh
grep -q -- "download --minimal" "$tmp/reth_calls.log" || { echo "FAIL: expected download --minimal for empty datadir"; exit 1; }

# Case 2: initialized datadir -> no download.
rm -f "$tmp/reth_calls.log"
mkdir -p "$tmp/full/db" && touch "$tmp/full/db/mdbx.dat"
DATADIR="$tmp/full" RETH_BIN="$tmp/reth" RETRY_DELAY=0 bash scripts/reth_init.sh
[ ! -f "$tmp/reth_calls.log" ] || { echo "FAIL: download must be skipped when db exists"; exit 1; }

# Case 3: failing download retries then errors.
cat > "$tmp/reth" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
chmod +x "$tmp/reth"
if DATADIR="$tmp/empty2" RETH_BIN="$tmp/reth" RETRY_DELAY=0 MAX_ATTEMPTS=2 bash scripts/reth_init.sh 2>/dev/null; then
  echo "FAIL: expected non-zero exit after exhausted retries"; exit 1
fi

echo "PASS test_reth_init"
