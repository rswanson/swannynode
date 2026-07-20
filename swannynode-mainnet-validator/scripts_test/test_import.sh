#!/usr/bin/env bash
# Tests fetch_and_import_validator.sh: pulls secrets, imports keystore and
# slashing data; skips entirely when the validator is already imported;
# fails hard when keystore secret is unavailable.
set -euo pipefail
cd "$(dirname "$0")/.."
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"

cat > "$tmp/bin/aws" <<EOF
#!/usr/bin/env bash
echo "aws \$@" >> "$tmp/calls.log"
# args: secretsmanager get-secret-value --secret-id <id> ...
[ -f "$tmp/SECRETS_FAIL" ] && exit 1
case "\$4" in
  */keystore) echo '{"version":4}';;
  */keystore-password) echo 'hunter2';;
  */slashing-protection) echo '{"metadata":{}}';;
esac
EOF
cat > "$tmp/bin/lighthouse" <<EOF
#!/usr/bin/env bash
echo "lighthouse \$@" >> "$tmp/calls.log"
# simulate a failing slashing-protection import when the marker is present
if [ -f "$tmp/SLASH_FAIL" ] && echo "\$@" | grep -q "slashing-protection import"; then exit 1; fi
EOF
cat > "$tmp/bin/chown" <<EOF
#!/usr/bin/env bash
echo "chown \$@" >> "$tmp/calls.log"
EOF
chmod +x "$tmp/bin/"*

# Case 1: fresh datadir -> import + slashing import run; sentinel written.
echo -n > "$tmp/calls.log"
PATH="$tmp/bin:$PATH" DATADIR="$tmp/lh" LH_BIN="$tmp/bin/lighthouse" bash scripts/fetch_and_import_validator.sh
grep -q "account validator import" "$tmp/calls.log" || { echo "FAIL: keystore import not invoked"; exit 1; }
grep -q "slashing-protection import" "$tmp/calls.log" || { echo "FAIL: slashing import not invoked"; exit 1; }
grep -q "chown -R lighthouse:eth" "$tmp/calls.log" || { echo "FAIL: ownership not fixed"; exit 1; }
[ -f "$tmp/lh/.validator-import-complete" ] || { echo "FAIL: sentinel not written after full import"; exit 1; }

# Case 2: already imported (sentinel present) -> no-op.
mkdir -p "$tmp/lh2" && touch "$tmp/lh2/.validator-import-complete"
echo -n > "$tmp/calls.log"
PATH="$tmp/bin:$PATH" DATADIR="$tmp/lh2" LH_BIN="$tmp/bin/lighthouse" bash scripts/fetch_and_import_validator.sh
grep -q "lighthouse" "$tmp/calls.log" && { echo "FAIL: must skip when already imported"; exit 1; }

# Case 2b (regression): partial first run — slashing import fails -> script
# fails, NO sentinel; the re-run must retry the imports, not short-circuit.
touch "$tmp/SLASH_FAIL"
if PATH="$tmp/bin:$PATH" DATADIR="$tmp/lh2b" LH_BIN="$tmp/bin/lighthouse" bash scripts/fetch_and_import_validator.sh 2>/dev/null; then
  echo "FAIL: expected failure when slashing import fails"; exit 1
fi
[ ! -f "$tmp/lh2b/.validator-import-complete" ] || { echo "FAIL: sentinel must not exist after partial import"; exit 1; }
rm -f "$tmp/SLASH_FAIL"
echo -n > "$tmp/calls.log"
PATH="$tmp/bin:$PATH" DATADIR="$tmp/lh2b" LH_BIN="$tmp/bin/lighthouse" bash scripts/fetch_and_import_validator.sh
grep -q "slashing-protection import" "$tmp/calls.log" || { echo "FAIL: re-run must retry slashing import"; exit 1; }
[ -f "$tmp/lh2b/.validator-import-complete" ] || { echo "FAIL: sentinel missing after successful retry"; exit 1; }

# Case 3: secrets unavailable -> non-zero exit (gates the vc from starting).
touch "$tmp/SECRETS_FAIL"
if PATH="$tmp/bin:$PATH" DATADIR="$tmp/lh3" LH_BIN="$tmp/bin/lighthouse" bash scripts/fetch_and_import_validator.sh 2>/dev/null; then
  echo "FAIL: expected failure when secrets are unavailable"; exit 1
fi

echo "PASS test_import"
