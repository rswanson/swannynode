#!/usr/bin/env bash
# Near-zero-downtime validator cutover from OLD host to NEW host.
#
# Run this from the operator workstation, NOT on either node.
#
#   OLD_HOST=1.2.3.4 NEW_HOST=5.6.7.8 SSH_KEY=~/.ssh/key ./cutover_validator.sh
#
# THE ONLY THING THAT PREVENTS A SLASHING EVENT HERE IS ORDERING:
#   1. the old VC is stopped AND disabled AND verified dead, THEN
#   2. its slashing-protection DB is exported at that final state, THEN
#   3. that export is imported on the new host, THEN
#   4. the new VC starts.
# Every step is a hard gate. If any fails, the script aborts WITHOUT starting
# the new VC and tells you how to roll back. Never reorder these steps, and
# never comment out a gate to "just get it done" — a double-signature costs
# far more than an hour of missed attestations.
#
# Expected signing gap: ~15-40s (1-4 slots). The old VC keeps attesting right
# up to step 1, and the new node is already fully synced before you run this.
set -euo pipefail

OLD_HOST="${OLD_HOST:?OLD_HOST (current validator public IP) is required}"
NEW_HOST="${NEW_HOST:?NEW_HOST (new validator public IP) is required}"
SSH_KEY="${SSH_KEY:?SSH_KEY (path to private key) is required}"
SSH_USER="${SSH_USER:-ubuntu}"
NEW_VALIDATOR_DATADIR="${NEW_VALIDATOR_DATADIR:-/validator/lighthouse}"
OLD_VALIDATOR_DATADIR="${OLD_VALIDATOR_DATADIR:-/data/mainnet/lighthouse}"
SECRET_PREFIX="${SECRET_PREFIX:-mainnet-validator}"
# Persistent, operator-owned home for the validated interchange export. Must
# NOT live under $WORK: that directory is removed by the EXIT trap below, so
# anything the closing instructions ask the operator to act on afterwards has
# to survive outside it.
CUTOVER_PERSIST_DIR="${CUTOVER_PERSIST_DIR:-$HOME/.swannynode/validator-cutover}"
# Guards against two concurrent invocations (two operators, or one operator
# re-running after a dropped connection) both reaching step 4 and starting a
# second live signer. See the lock acquisition in PREFLIGHT.
LOCK_DIR="/run/lighthousevalidator-cutover.lock"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

SSH_OPTS=(-i "$SSH_KEY" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10)
old() { ssh "${SSH_OPTS[@]}" "$SSH_USER@$OLD_HOST" "$@"; }
new() { ssh "${SSH_OPTS[@]}" "$SSH_USER@$NEW_HOST" "$@"; }
say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() { printf '\n\033[31mABORT: %s\033[0m\n' "$*" >&2; exit 1; }

# Best-effort release of the new-host cutover lock. Only wired into the EXIT
# trap once the lock is actually held (see PREFLIGHT), so a failed
# ACQUISITION never tries to release a lock we don't own. A failed release is
# reported loudly but must never itself be treated as fatal — we're already
# on our way out.
release_lock() {
  new "sudo rmdir $LOCK_DIR" >/dev/null 2>&1 \
    || echo "WARNING: could not release cutover lock $LOCK_DIR on $NEW_HOST — remove it manually before the next cutover run (ssh in and rmdir it after confirming no cutover is actually in flight)." >&2
}

# Positively confirms a systemd unit on the NEW host is inactive. Any
# ambiguity — including an unreachable host — is treated as "not confirmed",
# never as "confirmed inactive". Used only on the step-4 failure path, where
# mistaking an unknown state for a safe one could lead an operator to roll
# back onto a host that is still live.
new_unit_confirmed_inactive() {
  local unit="$1" state
  state=$(new "systemctl is-active $unit" 2>/dev/null) || true
  case "$state" in
    inactive|failed|unknown) return 0 ;;
    *) return 1 ;;
  esac
}

################################################################################
say "PREFLIGHT (no downtime — nothing is stopped yet)"
################################################################################

# --- claim an exclusive lock on the new host BEFORE any other check. Two
#     operators racing this script, or one operator re-running it after a
#     dropped connection, must not both reach step 4 and start two live
#     signers. mkdir is atomic on a local filesystem, so this is a safe
#     test-and-set even under a race.
new "sudo mkdir $LOCK_DIR" \
  || die "could not acquire the cutover lock ($LOCK_DIR) on $NEW_HOST — another cutover may already be running, or a previous run left the lock behind. Investigate before proceeding: ssh $SSH_USER@$NEW_HOST 'ls -ld $LOCK_DIR; systemctl is-active lighthousevalidator lighthousevalidator-cutover'. Only rmdir it yourself once you have personally confirmed no other cutover is in flight."
trap 'release_lock; rm -rf "$WORK"' EXIT

# --- new node must be fully synced, or we cut over onto a node that cannot attest
sync_json=$(new "curl -s --max-time 10 localhost:5052/eth/v1/node/syncing") \
  || die "new host beacon node is not answering on :5052"
echo "new BN syncing: $sync_json"
grep -q '"is_syncing":false'   <<<"$sync_json" || die "new BN is still syncing"
grep -q '"is_optimistic":false' <<<"$sync_json" || die "new BN head is optimistic (EL not caught up)"
grep -q '"el_offline":false'    <<<"$sync_json" || die "new BN reports execution layer offline"

peers=$(new "curl -s --max-time 10 localhost:5052/eth/v1/node/peer_count" \
  | sed 's/.*"connected":"\([0-9]*\)".*/\1/')
echo "new BN peers: $peers"
[ "${peers:-0}" -ge 20 ] || die "new BN has only ${peers:-0} peers; too few to attest reliably"

# --- new node must already hold the keystore (imported at bootstrap), because
#     fetching keys during the cutover window would blow the downtime budget
new "sudo test -f $NEW_VALIDATOR_DATADIR/validator_definitions.yml" \
  || die "new host has no validator_definitions.yml — run validator-init first"

# --- and neither the real unit NOR a leftover/concurrent transient cutover
#     unit may be running. Two live VCs on one key is the exact failure this
#     whole procedure exists to prevent. (The lock above should already
#     prevent a genuine concurrent run from reaching this point, but this
#     check also catches a stale unit left by a previous failed/aborted run.)
new "systemctl is-active --quiet lighthousevalidator" \
  && die "new host VC is ALREADY RUNNING — stop it before cutover"
new "systemctl is-active --quiet lighthousevalidator-cutover" \
  && die "new host has an ACTIVE lighthousevalidator-cutover unit already — a previous or concurrent cutover may still be running. Investigate before proceeding; do not stop it blindly without understanding why it's there."
echo "new host VC: stopped (correct)"

# --- old node must currently be the one attesting
old "systemctl is-active --quiet lighthousevalidator" \
  || die "old host VC is not running — is the cutover already done? Investigate before proceeding."
echo "old host VC: running (correct)"

say "Preflight passed. The signing gap starts NOW."
GAP_START=$(date +%s)

################################################################################
say "STEP 1/4 — stop and DISABLE the old validator client"
################################################################################
# disable, not just stop: the unit is Restart=always and enabled at boot, so a
# stop alone would let a reboot silently resurrect a second signer.
old "sudo systemctl disable --now lighthousevalidator"

# Hard gate. Belt and braces: unit state AND process table.
old "systemctl is-active --quiet lighthousevalidator" \
  && die "old VC STILL ACTIVE after disable --now. Do NOT start the new VC."
old "pgrep -af 'lighthouse[[:space:]]+vc' >/dev/null" \
  && die "a 'lighthouse vc' process is STILL RUNNING on the old host. Do NOT start the new VC."
echo "old VC confirmed stopped and disabled"

################################################################################
say "STEP 2/4 — export slashing protection at its final state"
################################################################################
# Must happen AFTER the VC stops: the DB is locked while it runs, and an export
# taken earlier would miss any attestation signed in between.
old "sudo -u lighthouse /data/bin/lighthouse account validator slashing-protection export \
      /tmp/interchange-cutover.json --network mainnet --datadir $OLD_VALIDATOR_DATADIR" \
  || die "slashing-protection export FAILED. Roll back with: ssh $SSH_USER@$OLD_HOST sudo systemctl enable --now lighthousevalidator"

scp "${SSH_OPTS[@]}" -q "$SSH_USER@$OLD_HOST:/tmp/interchange-cutover.json" "$WORK/interchange.json" \
  || die "could not fetch interchange. Roll back with: ssh $SSH_USER@$OLD_HOST sudo systemctl enable --now lighthousevalidator"

# A truncated or empty interchange would import "successfully" and protect nothing.
python3 -c "
import json,sys
d=json.load(open('$WORK/interchange.json'))
n=len(d.get('data',[]))
if n==0: sys.exit('interchange contains ZERO validators')
print(f'interchange OK: {n} validator(s), genesis_validators_root present={bool(d[\"metadata\"][\"genesis_validators_root\"])}')
" || die "interchange failed validation. Roll back with: ssh $SSH_USER@$OLD_HOST sudo systemctl enable --now lighthousevalidator"

# Copy the now-validated interchange out of $WORK to a persistent,
# operator-owned path BEFORE doing anything else that could fail and exit
# the script. $WORK is deleted by the EXIT trap, so this is the only copy
# that survives — including for a DR-secret refresh done by hand later if
# the automatic refresh at the end of this script fails.
mkdir -p "$CUTOVER_PERSIST_DIR"
chmod 700 "$CUTOVER_PERSIST_DIR"
PERSIST_FILE="$CUTOVER_PERSIST_DIR/interchange-$(date -u +%Y%m%dT%H%M%SZ).json"
cp "$WORK/interchange.json" "$PERSIST_FILE"
chmod 600 "$PERSIST_FILE"
echo "validated interchange persisted to $PERSIST_FILE (this survives the script's exit; $WORK does not)"

################################################################################
say "STEP 3/4 — import slashing protection on the new host"
################################################################################
scp "${SSH_OPTS[@]}" -q "$WORK/interchange.json" "$SSH_USER@$NEW_HOST:/tmp/interchange-cutover.json"

# Lighthouse merges on import and refuses to lower a watermark, so importing
# over the stale copy that validator-init seeded is safe and strictly raises
# protection.
new "sudo -u lighthouse /data/bin/lighthouse account validator slashing-protection import \
      /tmp/interchange-cutover.json --network mainnet --datadir $NEW_VALIDATOR_DATADIR" \
  || die "slashing import FAILED on new host. New VC NOT started. Roll back with: ssh $SSH_USER@$OLD_HOST sudo systemctl enable --now lighthousevalidator"

################################################################################
say "STEP 4/4 — start the new validator client"
################################################################################
# DOPPELGANGER=off for this one start only: doppelganger costs ~2 epochs
# (~13 min) of deliberate silence, which is the entire downtime budget. It is
# a safety net against forgetting another signer — and steps 1-3 just proved
# that mechanically. The unit reverts to doppelganger-on for every later start.
#
# Restart=on-failure (deliberately NOT Restart=always): this transient unit
# runs for several minutes — until the operator swaps it for the real,
# doppelganger-protected unit in the closing instructions — with doppelganger
# disabled. If the process crashes in that window, restarting it is strictly
# better than a silent signing stop with nothing to recover it: steps 1-3
# already mechanically proved no other signer exists, and systemd re-applies
# the same --setenv on every restart of this unit, so the restart is still
# doppelganger-off — re-arming doppelganger here would just buy another ~13
# minutes of silence for no added safety. Restart=always would additionally
# restart on a clean, intentional exit (status 0); systemd already treats an
# explicit `systemctl stop` (the closing-instructions swap-over) as
# "requested" and won't restart for it under either policy, but on-failure
# keeps that explicit and avoids masking a process that exits 0 on its own
# for some unrelated reason.
if ! new "sudo systemctl enable lighthousevalidator && sudo systemd-run --unit=lighthousevalidator-cutover \
      --property=EnvironmentFile=/etc/swannynode/validator.env \
      --property=User=lighthouse --property=Group=eth \
      --property=Restart=on-failure --property=RestartSec=5 \
      --setenv=DOPPELGANGER=off /opt/swannynode/scripts/start_lighthouse_vc.sh"; then
  say "STEP 4 FAILED — verifying new-host state before giving ANY rollback advice"
  # This failure can mean the SSH call itself dropped AFTER the unit actually
  # started on $NEW_HOST but BEFORE the exit status came back. Printing
  # "roll back, re-enable the old VC" unconditionally here — if the new unit
  # is in fact running, with doppelganger OFF — would produce two live
  # signers. So: force-stop both possible new-host units ourselves, then
  # POSITIVELY CONFIRM both are inactive before any rollback guidance is
  # given. An unreachable host is an UNKNOWN state, never "confirmed
  # inactive".
  new "sudo systemctl stop lighthousevalidator-cutover lighthousevalidator" >/dev/null 2>&1 || true

  cutover_inactive=0; real_inactive=0
  if new_unit_confirmed_inactive lighthousevalidator-cutover; then cutover_inactive=1; fi
  if new_unit_confirmed_inactive lighthousevalidator; then real_inactive=1; fi

  if [ "$cutover_inactive" = 1 ] && [ "$real_inactive" = 1 ]; then
    die "new VC start failed, and $NEW_HOST is CONFIRMED STOPPED (both lighthousevalidator-cutover and lighthousevalidator verified inactive). The old host's VC has remained stopped and disabled throughout. Only now is it safe to roll back: ssh $SSH_USER@$OLD_HOST sudo systemctl enable --now lighthousevalidator"
  else
    die "new VC start failed and we could NOT confirm $NEW_HOST is fully stopped (lighthousevalidator-cutover confirmed-inactive=$cutover_inactive, lighthousevalidator confirmed-inactive=$real_inactive). DO NOT re-enable the old host — that risks TWO LIVE SIGNERS, one of them with doppelganger disabled. STOP and manually verify: ssh $SSH_USER@$NEW_HOST 'systemctl is-active lighthousevalidator-cutover lighthousevalidator'. Only after YOU personally confirm BOTH are inactive should you roll back: ssh $SSH_USER@$OLD_HOST sudo systemctl enable --now lighthousevalidator"
  fi
fi

GAP_END=$(date +%s)
say "Signing gap: $((GAP_END - GAP_START))s (~$(( (GAP_END - GAP_START) / 12 + 1 )) slots)"

################################################################################
say "VERIFY"
################################################################################
sleep 20
new "sudo journalctl -u lighthousevalidator-cutover --since '2 min ago' --no-pager | tail -20"

################################################################################
say "DISASTER-RECOVERY REFRESH — updating the Secrets Manager copy of slashing protection"
################################################################################
# The DR copy is what a REBUILT host re-imports. Leaving it stale here is
# exactly how a regressed slashing DB could later reach a running validator.
# Attempt the refresh directly so it isn't a manual step someone forgets to
# do (or does using a $WORK path that's already been deleted). Never let a
# failure here be silent, and never let it be mistaken for a cutover failure
# — by this point the new VC is already live and attesting.
DR_SECRET_ID="$SECRET_PREFIX/slashing-protection"
DR_REFRESHED=0
if command -v aws >/dev/null 2>&1; then
  if aws secretsmanager put-secret-value --secret-id "$DR_SECRET_ID" \
       --secret-string "file://$PERSIST_FILE" >/dev/null 2>"$WORK/aws-error.log"; then
    DR_REFRESHED=1
    echo "DR secret '$DR_SECRET_ID' refreshed from $PERSIST_FILE"
  else
    printf '\033[31mWARNING: automatic DR secret refresh FAILED (see error below). This does NOT affect the live validator, but you MUST fix it before relying on disaster recovery.\033[0m\n'
    cat "$WORK/aws-error.log" >&2 || true
  fi
else
  printf '\033[31mWARNING: aws CLI not found on this workstation — the DR secret was NOT refreshed automatically.\033[0m\n'
fi

if [ "$DR_REFRESHED" = 1 ]; then
  DR_STATUS_LINE="   DONE automatically by this script."
else
  DR_STATUS_LINE="   NOT done — run this yourself now:
     aws secretsmanager put-secret-value --secret-id $DR_SECRET_ID --secret-string file://$PERSIST_FILE"
fi

cat <<EOF

$(printf '\033[1mCUTOVER COMPLETE — now do these, in order:\033[0m')

1. Watch for the first successful attestation (up to ~6.4 min for this
   validator's slot to come round):
     ssh $SSH_USER@$NEW_HOST sudo journalctl -u lighthousevalidator-cutover -f
   Confirm on beaconcha.in before going further.

2. Disaster-recovery copy of slashing protection (validated interchange
   persisted at: $PERSIST_FILE — this file survives, unlike anything under
   the now-deleted $WORK):
$DR_STATUS_LINE

3. Swap the transient cutover unit for the real one, so the VC survives reboot
   and runs WITH doppelganger protection from here on:
     ssh $SSH_USER@$NEW_HOST 'sudo systemctl stop lighthousevalidator-cutover && sudo systemctl start lighthousevalidator'
   Expect ~13 min of silence on that restart — that is doppelganger doing its job.

4. Only once steps 1-3 are confirmed, terminate the old instance. Until then
   it is your rollback path. Do NOT re-enable its VC.

The cutover lock on $NEW_HOST ($LOCK_DIR) is released automatically as this
script exits. If a future run reports the lock is already held, verify no
cutover is actually in flight before removing it by hand.

EOF
