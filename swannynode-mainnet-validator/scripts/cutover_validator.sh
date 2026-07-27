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
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

SSH_OPTS=(-i "$SSH_KEY" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10)
old() { ssh "${SSH_OPTS[@]}" "$SSH_USER@$OLD_HOST" "$@"; }
new() { ssh "${SSH_OPTS[@]}" "$SSH_USER@$NEW_HOST" "$@"; }
say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() { printf '\n\033[31mABORT: %s\033[0m\n' "$*" >&2; exit 1; }

################################################################################
say "PREFLIGHT (no downtime — nothing is stopped yet)"
################################################################################

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

# --- and its VC must NOT be running. Two live VCs on one key is the exact
#     failure this whole procedure exists to prevent.
new "systemctl is-active --quiet lighthousevalidator" \
  && die "new host VC is ALREADY RUNNING — stop it before cutover"
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
new "sudo systemctl enable lighthousevalidator && sudo systemd-run --unit=lighthousevalidator-cutover \
      --property=EnvironmentFile=/etc/swannynode/validator.env \
      --property=User=lighthouse --property=Group=eth \
      --setenv=DOPPELGANGER=off /data/scripts/start_lighthouse_vc.sh" \
  || die "new VC failed to start. Old VC is still stopped — roll back NOW: ssh $SSH_USER@$OLD_HOST sudo systemctl enable --now lighthousevalidator"

GAP_END=$(date +%s)
say "Signing gap: $((GAP_END - GAP_START))s (~$(( (GAP_END - GAP_START) / 12 + 1 )) slots)"

################################################################################
say "VERIFY"
################################################################################
sleep 20
new "sudo journalctl -u lighthousevalidator-cutover --since '2 min ago' --no-pager | tail -20"

cat <<EOF

$(printf '\033[1mCUTOVER COMPLETE — now do these, in order:\033[0m')

1. Watch for the first successful attestation (up to ~6.4 min for this
   validator's slot to come round):
     ssh $SSH_USER@$NEW_HOST sudo journalctl -u lighthousevalidator-cutover -f
   Confirm on beaconcha.in before going further.

2. Refresh the disaster-recovery copy of slashing protection — the one in
   Secrets Manager is now stale, and a rebuild that imported it would regress
   protection:
     aws secretsmanager put-secret-value --secret-id mainnet-validator/slashing-protection \\
       --secret-string file://$WORK/interchange.json
   (copy the file out of $WORK first — this script deletes it on exit)

3. Swap the transient cutover unit for the real one, so the VC survives reboot
   and runs WITH doppelganger protection from here on:
     ssh $SSH_USER@$NEW_HOST 'sudo systemctl stop lighthousevalidator-cutover && sudo systemctl start lighthousevalidator'
   Expect ~13 min of silence on that restart — that is doppelganger doing its job.

4. Only once steps 1-3 are confirmed, terminate the old instance. Until then
   it is your rollback path. Do NOT re-enable its VC.

EOF
