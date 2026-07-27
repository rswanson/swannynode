#!/usr/bin/env bash
# Tests cutover_validator.sh: the script that moves signing authority for a
# live mainnet validator between hosts. A double-signature here is
# unrecoverable, so this suite pins the properties that keep it that way:
#
#   - the new VC is NEVER started when the old-host death check fails
#   - the new VC is NEVER started when the slashing-protection export or
#     import fails
#   - an empty or malformed interchange aborts BEFORE import
#   - the hard gates run in the required order (lock -> stop old -> export ->
#     transfer -> import -> start new)
#   - a second concurrent run cannot reach step 4 (new-host lock)
#   - if step 4's SSH call fails after the new unit may have actually
#     started, the script verifies (and if needed stops) the new host before
#     giving ANY rollback advice, and never tells the operator to roll back
#     unless that verification actually succeeded
#   - the validated interchange is persisted outside the mktemp workdir that
#     gets deleted on exit
#
# No network access occurs: ssh, scp and aws are all stubbed. Remote state
# (which units are "active" on which host) is simulated with marker files
# under $STATE and mutated by the stubs exactly like the real units would be.
set -euo pipefail
cd "$(dirname "$0")/.."

BIN="$(mktemp -d)"
trap 'rm -rf "$BIN"' EXIT

# --- stub: ssh -------------------------------------------------------------
# Simulates both the OLD and NEW hosts. $STATE (exported by the test harness
# before each run) holds the simulated remote state and the shared call log.
cat > "$BIN/ssh" <<'EOF'
#!/usr/bin/env bash
set -u
: "${STATE:?STATE must be set by the test harness}"
echo "ssh $*" >> "$STATE/calls.log"

args=("$@")
n=${#args[@]}
cmd="${args[$((n-1))]}"
target="${args[$((n-2))]}"

case "$target" in
  *@old-host) host=old ;;
  *@new-host) host=new ;;
  *) echo "STUB ssh: unrecognized target '$target'" >&2; exit 97 ;;
esac

case "$cmd" in
  *"mkdir /run/lighthousevalidator-cutover.lock"*)
    [ -f "$STATE/lock_held" ] && exit 1
    touch "$STATE/lock_held"; exit 0 ;;
  *"rmdir /run/lighthousevalidator-cutover.lock"*)
    rm -f "$STATE/lock_held"; exit 0 ;;
  *"eth/v1/node/syncing"*)
    printf '{"data":{"is_syncing":false,"is_optimistic":false,"el_offline":false}}'
    exit 0 ;;
  *"eth/v1/node/peer_count"*)
    printf '{"data":{"connected":"30"}}'
    exit 0 ;;
  *"validator_definitions.yml"*)
    exit 0 ;;
  *"is-active --quiet lighthousevalidator-cutover"*)
    [ -f "$STATE/new_cutover_active" ] && exit 0 || exit 1 ;;
  *"is-active lighthousevalidator-cutover"*)
    if [ -f "$STATE/new_cutover_active" ]; then echo active; exit 0; else echo inactive; exit 3; fi ;;
  *"is-active --quiet lighthousevalidator"*)
    if [ "$host" = old ]; then
      [ -f "$STATE/old_vc_active" ] && exit 0 || exit 1
    else
      [ -f "$STATE/new_vc_active" ] && exit 0 || exit 1
    fi ;;
  *"is-active lighthousevalidator"*)
    if [ "$host" = old ]; then
      if [ -f "$STATE/old_vc_active" ]; then echo active; exit 0; else echo inactive; exit 3; fi
    else
      if [ -f "$STATE/new_vc_active" ]; then echo active; exit 0; else echo inactive; exit 3; fi
    fi ;;
  *"systemctl disable --now lighthousevalidator"*)
    [ -f "$STATE/FLAG_DISABLE_FAILS" ] || rm -f "$STATE/old_vc_active"
    exit 0 ;;
  *"pgrep -af"*)
    [ -f "$STATE/FLAG_OLD_PGREP_ALIVE" ] && exit 0 || exit 1 ;;
  *"slashing-protection export"*)
    [ -f "$STATE/FLAG_EXPORT_FAILS" ] && exit 1
    exit 0 ;;
  *"slashing-protection import"*)
    [ -f "$STATE/FLAG_IMPORT_FAILS" ] && exit 1
    exit 0 ;;
  *"systemctl enable lighthousevalidator && sudo systemd-run"*)
    if [ -f "$STATE/FLAG_STEP4_FAILS" ]; then
      [ -f "$STATE/FLAG_STEP4_UNIT_ACTUALLY_STARTED" ] && touch "$STATE/new_cutover_active"
      exit 1
    fi
    touch "$STATE/new_cutover_active"
    exit 0 ;;
  *"systemctl stop lighthousevalidator-cutover lighthousevalidator"*)
    [ -f "$STATE/FLAG_STEP4_STOP_FAILS" ] && exit 1
    rm -f "$STATE/new_cutover_active" "$STATE/new_vc_active"
    exit 0 ;;
  *"journalctl -u lighthousevalidator-cutover"*)
    echo "fake log line"; exit 0 ;;
  *)
    echo "STUB ssh: unhandled command on $host host: $cmd" >&2
    exit 98 ;;
esac
EOF
chmod +x "$BIN/ssh"

# --- stub: scp ---------------------------------------------------------
# Download (old-host -> local $WORK/interchange.json): writes controllable
# interchange content. Upload (local -> new-host): just logs.
cat > "$BIN/scp" <<'EOF'
#!/usr/bin/env bash
set -u
: "${STATE:?STATE must be set by the test harness}"
echo "scp $*" >> "$STATE/calls.log"

args=("$@")
n=${#args[@]}
src="${args[$((n-2))]}"
dst="${args[$((n-1))]}"

case "$src" in
  *old-host:*)
    [ -f "$STATE/FLAG_SCP_FETCH_FAILS" ] && exit 1
    if [ -f "$STATE/FLAG_EMPTY_INTERCHANGE" ]; then
      printf '{"metadata":{"genesis_validators_root":"0x00"},"data":[]}' > "$dst"
    elif [ -f "$STATE/FLAG_MALFORMED_INTERCHANGE" ]; then
      printf 'not-json{{{' > "$dst"
    else
      printf '{"metadata":{"genesis_validators_root":"0xabc"},"data":[{"pubkey":"0x1"}]}' > "$dst"
    fi
    exit 0 ;;
  *)
    [ -f "$STATE/FLAG_SCP_PUSH_FAILS" ] && exit 1
    exit 0 ;;
esac
EOF
chmod +x "$BIN/scp"

# --- stub: aws -----------------------------------------------------------
cat > "$BIN/aws" <<'EOF'
#!/usr/bin/env bash
set -u
: "${STATE:?STATE must be set by the test harness}"
echo "aws $*" >> "$STATE/calls.log"
[ -f "$STATE/FLAG_AWS_PUT_FAILS" ] && exit 1
exit 0
EOF
chmod +x "$BIN/aws"

# --- stub: sleep (skip the real 20s VERIFY delay) -------------------------
cat > "$BIN/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$BIN/sleep"

################################################################################
# Test harness
################################################################################

CASE_DIRS=()
cleanup_cases() { local d; for d in "${CASE_DIRS[@]:-}"; do [ -n "$d" ] && rm -rf "$d"; done; }
trap 'cleanup_cases; rm -rf "$BIN"' EXIT

fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }

# Fresh simulated world: old VC running (correct starting state), new VC and
# new cutover unit stopped, no lock held.
new_case() {
  STATE="$(mktemp -d)"
  CASE_DIRS+=("$STATE")
  mkdir -p "$STATE/persist"
  : > "$STATE/calls.log"
  touch "$STATE/old_vc_active"
}

run_cutover() {
  set +e
  PATH="$BIN:$PATH" STATE="$STATE" \
    OLD_HOST=old-host NEW_HOST=new-host SSH_USER=ubuntu SSH_KEY=/dev/null \
    CUTOVER_PERSIST_DIR="$STATE/persist" \
    bash scripts/cutover_validator.sh >"$STATE/output.log" 2>&1
  echo "$?" > "$STATE/exit_code"
  set -e
}

exit_code() { cat "$STATE/exit_code"; }

assert_exit_zero() {
  [ "$(exit_code)" = "0" ] || fail "$1: expected exit 0, got $(exit_code). Output:
$(cat "$STATE/output.log")"
}
assert_exit_nonzero() {
  [ "$(exit_code)" != "0" ] || fail "$1: expected non-zero exit, got 0. Output:
$(cat "$STATE/output.log")"
}
assert_log_contains() {
  grep -qF -- "$2" "$STATE/calls.log" || fail "$1: expected calls.log to contain: $2
calls.log:
$(cat "$STATE/calls.log")"
}
assert_log_not_contains() {
  if grep -qF -- "$2" "$STATE/calls.log"; then
    fail "$1: expected calls.log to NOT contain: $2
calls.log:
$(cat "$STATE/calls.log")"
  fi
}
assert_output_contains() {
  grep -qF -- "$2" "$STATE/output.log" || fail "$1: expected output to contain: $2
output:
$(cat "$STATE/output.log")"
}
assert_output_not_contains() {
  if grep -qF -- "$2" "$STATE/output.log"; then
    fail "$1: expected output to NOT contain: $2
output:
$(cat "$STATE/output.log")"
  fi
}
log_line_of() { grep -n -F -- "$1" "$STATE/calls.log" | head -1 | cut -d: -f1; }
assert_order() {
  local desc="$1" first="$2" second="$3" l1 l2
  l1=$(log_line_of "$first"); l2=$(log_line_of "$second")
  [ -n "$l1" ] || fail "$desc: '$first' not found in calls.log"
  [ -n "$l2" ] || fail "$desc: '$second' not found in calls.log"
  [ "$l1" -lt "$l2" ] || fail "$desc: expected '$first' (line $l1) before '$second' (line $l2)"
}
assert_persist_file_created() {
  local n; n=$(find "$STATE/persist" -maxdepth 1 -name 'interchange-*.json' | wc -l | tr -d ' ')
  [ "$n" -ge 1 ] || fail "$1: expected a persisted interchange file under $STATE/persist"
}
assert_persist_file_absent() {
  local n; n=$(find "$STATE/persist" -maxdepth 1 -name 'interchange-*.json' 2>/dev/null | wc -l | tr -d ' ')
  [ "$n" = 0 ] || fail "$1: expected NO persisted interchange file under $STATE/persist (validation should abort first), found $n"
}

################################################################################
# Case 1: happy path — every gate passes, new VC starts.
################################################################################
new_case
run_cutover
assert_exit_zero "happy path"

# Finding 9: transient unit must carry a restart policy.
assert_log_contains "happy path" "Restart=on-failure"

# Finding 7: the validated interchange must survive under a persistent,
# operator-owned path (not the deleted $WORK), and the DR secret refresh
# must actually be attempted against that path.
assert_persist_file_created "happy path"
assert_log_contains "happy path" "aws secretsmanager put-secret-value"
assert_log_contains "happy path" "file://$STATE/persist/interchange-"

# Finding 2: the lock must be acquired before ANYTHING else, and released on
# a clean exit; the new VC (transient cutover unit) must actually be up.
[ "$(log_line_of "mkdir /run/lighthousevalidator-cutover.lock")" = "1" ] \
  || fail "happy path: lock acquisition must be the very first remote action"
[ -f "$STATE/lock_held" ] && fail "happy path: lock must be released after a successful run"
[ -f "$STATE/new_cutover_active" ] || fail "happy path: new VC should be active after a successful run"

# Gates run in the required order.
assert_order "happy path" "systemctl disable --now lighthousevalidator" "slashing-protection export"
assert_order "happy path" "slashing-protection export" "old-host:/tmp/interchange-cutover.json"
assert_order "happy path" "old-host:/tmp/interchange-cutover.json" "new-host:/tmp/interchange-cutover.json"
assert_order "happy path" "new-host:/tmp/interchange-cutover.json" "slashing-protection import"
assert_order "happy path" "slashing-protection import" "systemd-run --unit=lighthousevalidator-cutover"
echo "PASS case: happy path"

################################################################################
# Case 2: old-host death check fails (pgrep still finds a live VC process
# after disable --now) -> new VC must NEVER start.
################################################################################
new_case
touch "$STATE/FLAG_OLD_PGREP_ALIVE"
run_cutover
assert_exit_nonzero "pgrep-alive death check"
assert_log_not_contains "pgrep-alive death check" "slashing-protection export"
assert_log_not_contains "pgrep-alive death check" "systemd-run --unit=lighthousevalidator-cutover"
echo "PASS case: old-host death check (pgrep) fails -> new VC never started"

################################################################################
# Case 3: old-host death check fails a different way (is-active still
# reports the old VC active right after disable --now).
################################################################################
new_case
touch "$STATE/FLAG_DISABLE_FAILS"
run_cutover
assert_exit_nonzero "disable-fails death check"
assert_log_not_contains "disable-fails death check" "slashing-protection export"
assert_log_not_contains "disable-fails death check" "systemd-run --unit=lighthousevalidator-cutover"
echo "PASS case: old-host death check (is-active) fails -> new VC never started"

################################################################################
# Case 4: slashing-protection export fails -> new VC must NEVER start.
################################################################################
new_case
touch "$STATE/FLAG_EXPORT_FAILS"
run_cutover
assert_exit_nonzero "export fails"
assert_log_not_contains "export fails" "old-host:/tmp/interchange-cutover.json"
assert_log_not_contains "export fails" "systemd-run --unit=lighthousevalidator-cutover"
echo "PASS case: slashing-protection export fails -> new VC never started"

################################################################################
# Case 5: slashing-protection import fails -> new VC must NEVER start.
################################################################################
new_case
touch "$STATE/FLAG_IMPORT_FAILS"
run_cutover
assert_exit_nonzero "import fails"
assert_log_contains "import fails" "slashing-protection export"
assert_log_contains "import fails" "old-host:/tmp/interchange-cutover.json"
assert_log_not_contains "import fails" "systemd-run --unit=lighthousevalidator-cutover"
echo "PASS case: slashing-protection import fails -> new VC never started"

################################################################################
# Case 6: malformed (non-JSON) interchange -> abort BEFORE import, before
# persisting anything, and before starting the new VC.
################################################################################
new_case
touch "$STATE/FLAG_MALFORMED_INTERCHANGE"
run_cutover
assert_exit_nonzero "malformed interchange"
assert_log_not_contains "malformed interchange" "slashing-protection import"
assert_log_not_contains "malformed interchange" "systemd-run --unit=lighthousevalidator-cutover"
assert_persist_file_absent "malformed interchange"
echo "PASS case: malformed interchange aborts before import"

################################################################################
# Case 7: empty interchange (valid JSON, zero validators) -> same as above.
################################################################################
new_case
touch "$STATE/FLAG_EMPTY_INTERCHANGE"
run_cutover
assert_exit_nonzero "empty interchange"
assert_log_not_contains "empty interchange" "slashing-protection import"
assert_log_not_contains "empty interchange" "systemd-run --unit=lighthousevalidator-cutover"
assert_persist_file_absent "empty interchange"
echo "PASS case: empty interchange aborts before import"

################################################################################
# Case 8 (Finding 2): a concurrent/leftover cutover lock on the new host
# blocks the run before the old host is ever touched.
################################################################################
new_case
touch "$STATE/lock_held"
run_cutover
assert_exit_nonzero "lock already held"
assert_log_not_contains "lock already held" "systemctl disable --now lighthousevalidator"
assert_log_not_contains "lock already held" "slashing-protection export"
[ -f "$STATE/lock_held" ] || fail "lock already held: a lock we never acquired must not be touched/released by this run"
echo "PASS case: concurrent lock blocks a second run before the old host is touched"

################################################################################
# Case 9 (Finding 2, core scenario): step 4's SSH call fails AFTER the unit
# actually started on the new host. The script must stop+verify the new host
# itself, and — since that verification succeeds here — it MAY then say it's
# safe to roll back.
################################################################################
new_case
touch "$STATE/FLAG_STEP4_FAILS"
touch "$STATE/FLAG_STEP4_UNIT_ACTUALLY_STARTED"
run_cutover
assert_exit_nonzero "step4 ssh-drop, verified stop"
assert_log_contains "step4 ssh-drop, verified stop" "systemctl stop lighthousevalidator-cutover lighthousevalidator"
assert_output_contains "step4 ssh-drop, verified stop" "CONFIRMED STOPPED"
assert_output_contains "step4 ssh-drop, verified stop" "safe to roll back"
assert_output_not_contains "step4 ssh-drop, verified stop" "DO NOT re-enable the old host"
[ -f "$STATE/new_cutover_active" ] && fail "step4 ssh-drop, verified stop: new VC must be confirmed stopped, not left running"
echo "PASS case: step4 SSH-drop with unit actually started, but verified stopped -> safe-rollback message only"

################################################################################
# Case 10 (Finding 2, the dangerous scenario): step 4's SSH call fails AFTER
# the unit actually started, AND the script's own attempt to stop it also
# fails. The script must NOT say it's safe to roll back — that would produce
# two live signers.
################################################################################
new_case
touch "$STATE/FLAG_STEP4_FAILS"
touch "$STATE/FLAG_STEP4_UNIT_ACTUALLY_STARTED"
touch "$STATE/FLAG_STEP4_STOP_FAILS"
run_cutover
assert_exit_nonzero "step4 ssh-drop, stop verification fails"
assert_output_contains "step4 ssh-drop, stop verification fails" "DO NOT re-enable the old host"
assert_output_not_contains "step4 ssh-drop, stop verification fails" "safe to roll back"
[ -f "$STATE/new_cutover_active" ] || fail "step4 ssh-drop, stop verification fails: test setup should still show the unit as active"
echo "PASS case: step4 SSH-drop where stop can't be verified -> refuses rollback advice"

################################################################################
# Case 11: step 4 fails cleanly — the unit never actually started. Verified
# stop trivially succeeds (nothing was running), so rollback guidance is safe
# to give.
################################################################################
new_case
touch "$STATE/FLAG_STEP4_FAILS"
run_cutover
assert_exit_nonzero "step4 clean failure"
assert_output_contains "step4 clean failure" "safe to roll back"
assert_output_not_contains "step4 clean failure" "DO NOT re-enable the old host"
echo "PASS case: step4 clean failure (never started) -> safe-rollback message"

echo "PASS test_cutover_validator"
