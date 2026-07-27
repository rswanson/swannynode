# swannynode-mainnet-validator

Mainnet Ethereum validator: reth `--minimal` + lighthouse bn/vc + mev-boost on one
Graviton EC2 instance, with validator key material on a persistent (protected) EBS
volume, keys in Secrets Manager, and daily DLM snapshots. Chain data lives on
ephemeral instance-store NVMe by default (see Storage layout and Cost below) — this
is a **performance** move, not a cost optimization. Design spec:
`../docs/specs/2026-07-19-mainnet-validator-design.md`.

## Configuration

| Key | Default | Notes |
|---|---|---|
| `aws:region` | — (set `us-east-2`) | |
| `az` | `us-east-2a` | change + snapshot-restore to move AZ |
| `instanceType` | `i8g.xlarge` | 4 vCPU / 32 GiB / 937 GB local NVMe |
| `useInstanceStore` | `true` | chain data on ephemeral NVMe. Set `false` to keep `/data` on EBS (needs an EBS-backed instance type) |
| `validatorVolumeSizeGb` | `20` | EBS volume for keystores + slashing protection. **Always EBS, never ephemeral.** Left at gp3 defaults — it sees a couple of small writes per epoch |
| `createSecrets` | `true` | set `false` on a migration stack running alongside the live one — secret names are account-unique and would collide |
| `volumeSizeGb` | `600` | chain-data EBS volume; ignored when `useInstanceStore` is true |
| `volumeIops` | `6000` | chain-data volume only. Matched to the `r8g.xlarge` EBS baseline ceiling (6,000 IOPS), which is the instance type the EBS path implies |
| `volumeThroughput` | `156` | chain-data volume only. Matched to the same ceiling (156.25 MB/s) |
| `rethVersion` | `v2.4.1` | GitHub release tag |
| `lighthouseVersion` | `v8.2.0` | GitHub release tag |
| `mevboostVersion` | `1.12` | GitHub release tag (no `v` in asset name) |
| `feeRecipient` | **required** | your reward address; verify against a past proposal on beaconcha.in for validator `0xa177d5c2…ee7ec` |
| `keyName` | **required** | existing EC2 key pair name |
| `sshKey` | **required (secret)** | private key matching `keyName` |
| `sshUser` | `ubuntu` | Canonical Ubuntu 24.04 arm64 AMI |

## Storage layout

The table below is the default, `useInstanceStore true`. Set it `false` to keep
chain data on EBS instead — in that mode there is no separate `/validator`
volume at all: `validatorDataDir()` in `types.go` puts keystores and the
slashing-protection DB under `/data/mainnet/lighthouse` on the same durable EBS
volume as chain data, since a second volume would be pure churn when `/data` is
already durable.

| Path | Backing | Contents | On instance loss |
|---|---|---|---|
| `/data` | ephemeral NVMe (`useInstanceStore true`) | reth db, lighthouse beacon db, binaries | **lost** — re-synced automatically (~2 h) |
| `/validator` | EBS (protected, snapshotted) — instance-store mode only | keystores, **slashing-protection DB** | survives |

Chain data is reproducible, so putting it on ephemeral storage costs recovery
time, not safety. The slashing-protection database is not reproducible — losing
it and re-importing a stale copy from Secrets Manager can produce a double
signature. That is why it lives on its own EBS volume in instance-store mode,
and why nothing under `/data` is ever authoritative for validator state.

## Cost

**This is not a cost optimization — treat it as a performance trade, not a
saving.** `i8g.xlarge` (instance-store, $250.54/mo) costs $78.52/mo more than
the `r8g.xlarge` ($172.02/mo) that the EBS chain-data path implies (see
`volumeIops`/`volumeThroughput` above — they're sized to that instance's EBS
baseline). That's partly offset by dropping the 600 GB gp3 chain-data volume
($48.00/mo) for the 20 GB validator-only volume ($2.40/mo), a $45.60/mo saving.
Net: **+$32.92/mo**. The reason to run this mode is the block-validation
latency problem described in the recovery runbook below, not cost.

## Near-zero-downtime migration to NVMe

The old node keeps attesting through the entire build and sync. Only step 4 has
a signing gap, and it is **~15-40 s (1-4 slots)**.

**The rule that keeps this safe: exactly one validator client may be running at
any instant.** The new host's vc unit is deliberately left *disabled* by
bootstrap — not merely stopped — so a reboot during the parallel window cannot
start a second signer. This happens on any host that has never completed a
boot before (no `/etc/swannynode/.vc-first-boot-done` marker on its root
volume yet — `deploy.go`); the disable is skipped on later bootstrap re-runs
(e.g. a client-version bump) once that marker exists, so a routine re-run
after cutover can never silently re-disable the live signer. `cutover_validator.sh`
also takes an exclusive lock on the new host
(`mkdir /run/lighthousevalidator-cutover.lock`) before touching anything, so
two operators racing this script — or one operator re-running it after a
dropped SSH connection — cannot both reach step 4 and start two live signers.

1. **Create the migration stack** (parallel to the live one; new EIP, new
   instance, reusing the same Secrets Manager entries):
   ```sh
   pulumi stack init mainnet-nvme
   pulumi config set aws:region us-east-2 --stack mainnet-nvme
   pulumi config set instanceType i8g.xlarge --stack mainnet-nvme
   pulumi config set useInstanceStore true   --stack mainnet-nvme
   pulumi config set createSecrets   false   --stack mainnet-nvme   # live stack owns them
   pulumi config set feeRecipient 0x501b2Fa292a0465bC2d0C92a9C88B483A9a2Cb29 --stack mainnet-nvme
   pulumi config set keyName swannynode --stack mainnet-nvme
   pulumi config set --secret sshKey --stack mainnet-nvme < /path/to/key
   pulumi up --stack mainnet-nvme
   ```
   `createSecrets false` is not optional. With it true, the second stack tries
   to create secrets that already exist, and a later `pulumi destroy` of the
   migration stack would schedule your key material for deletion.

2. **Wait for the new node to sync** (~2-3 h; zero validator downtime — the old
   node is still attesting). reth downloads a fresh snapshot, which also sheds
   the MDBX free-page bloat the old volume accumulated:
   ```sh
   ssh ubuntu@<new-ip> 'journalctl -u reth-init -f'
   ssh ubuntu@<new-ip> 'curl -s localhost:5052/eth/v1/node/syncing'
   ```

3. **Confirm the new node is actually faster before cutting over.** If block
   validation has not improved, the migration gains nothing and you should stop
   here rather than spend a signing gap on it:
   ```sh
   ssh ubuntu@<new-ip> "curl -s localhost:9001/metrics | grep -E 'state_root_duration|execution_duration'"
   ```
   Expect state-root duration well under 1 s (it was ~2.9 s on gp3).

4. **Cut over.** The script takes the lock, runs a full preflight (new BN
   synced/non-optimistic/EL-online, ≥20 peers, keystore already present, no VC
   already running anywhere), and gates every step of the actual cutover —
   it aborts without starting the new vc if anything fails, and on a step-4
   failure it positively confirms the new host's units are inactive before
   giving any rollback advice (never assuming "unreachable" means "safe"):
   ```sh
   OLD_HOST=<old-ip> NEW_HOST=<new-ip> SSH_KEY=~/.ssh/swannynode \
     ./scripts/cutover_validator.sh
   ```
   On success it persists the validated slashing-protection interchange to
   `~/.swannynode/validator-cutover/` on your workstation (outside the
   script's temp dir, so it survives the script exiting) and attempts the
   Secrets Manager DR refresh itself — you'll only need to run that by hand if
   the script prints a warning that it failed.

5. **Follow the script's closing instructions.** It tells you to:
   - confirm the first attestation on beaconcha.in;
   - check whether the automatic DR secret refresh succeeded (rerun the
     printed `aws secretsmanager put-secret-value` command yourself if it
     warned that it didn't);
   - **swap the transient cutover unit for the real one — this step is not
     automatic, you must run it**, so the vc survives a reboot and runs with
     doppelganger protection from here on:
     ```sh
     ssh ubuntu@<new-ip> 'sudo systemctl stop lighthousevalidator-cutover && sudo systemctl start lighthousevalidator'
     ```
     Expect ~13 min of silence on that restart — that's doppelganger doing its job.

6. **Only after confirming attestations**, decommission the old stack. The old
   instance is your rollback path until this point.

   The old (live) stack is the one that ran with `createSecrets true`, so its
   state holds the chain-data volume **and** the three key-material secrets,
   all `Protect`ed (`identity.go`). `pulumi destroy` refuses to touch any
   protected resource, so unprotect all four before destroying, not just the
   volume:
   ```sh
   pulumi stack --show-urns --stack mainnet   # list URNs to unprotect
   pulumi state unprotect <validator-data-volume-urn> --stack mainnet
   pulumi state unprotect <validator-secret-keystore-urn> --stack mainnet
   pulumi state unprotect <validator-secret-keystore-password-urn> --stack mainnet
   pulumi state unprotect <validator-secret-slashing-protection-urn> --stack mainnet
   pulumi destroy --stack mainnet
   ```
   All four are also `RetainOnDelete`, so `pulumi destroy` only forgets them
   in Pulumi's state — it does **not** call the AWS delete API. The secrets
   stay intact in Secrets Manager (the new `mainnet-nvme` stack only ever
   looked them up, it doesn't own them, and still needs them to exist), and
   the old 600 GB chain-data volume stays intact in EBS until you delete it
   by hand in the EC2 console once you are certain — see Cost above for what
   that recovers and what it doesn't.

   Then update the sibling stacks: `cd ../alarms && pulumi config set instanceId
   <new id> && pulumi up`, and the prometheus target IP in `../monitoring`.

## First deploy (migration from the failed host)

1. `pulumi preview` then `pulumi up`. Bootstrap leaves the vc **disabled** on
   this genuinely fresh host (no `.vc-first-boot-done` marker yet — see the
   migration section above for the exact mechanism), so it will not start even
   across a reboot. Independently, `validator-init` also fails fast until
   secrets exist — a second, unrelated safety gate.
2. Export the slashing-protection interchange locally (Docker; backup lives in
   `~/validator-backup-2026-07-19/`):
   ```sh
   mkdir -p /tmp/lh-export && tar xzf ~/validator-backup-2026-07-19/lighthouse-mainnet-validators.tar.gz -C /tmp/lh-export
   docker run --rm -v /tmp/lh-export:/root/.lighthouse/mainnet sigp/lighthouse:v8.2.0 \
     lighthouse account validator slashing-protection export /root/.lighthouse/mainnet/interchange.json --network mainnet
   ```
3. Push the three secrets (values never enter Pulumi state):
   ```sh
   V=/tmp/lh-export/validators
   KS=$V/0xa177d5c28a60469bd6fe28255cc510f92ce9efd94bff28e49af97f0f84dd6b69a1262cc0fda6a10d672524f0735ee7ec/voting-keystore.json
   aws secretsmanager put-secret-value --secret-id mainnet-validator/keystore --secret-string file://$KS
   # password: the voting_keystore_password value from $V/validator_definitions.yml
   aws secretsmanager put-secret-value --secret-id mainnet-validator/keystore-password --secret-string "$(grep voting_keystore_password $V/validator_definitions.yml | awk '{print $2}')"
   aws secretsmanager put-secret-value --secret-id mainnet-validator/slashing-protection --secret-string file:///tmp/lh-export/interchange.json
   rm -rf /tmp/lh-export
   ```
4. Wait for sync: `journalctl -u reth-init -f` (snapshot download), then
   `curl -s localhost:5052/eth/v1/node/syncing` on the box until `is_syncing: false`.
5. **Stop the old instance** (`3.145.206.252`) in the EC2 console.
6. Retry the import now that secrets exist, then enable **and** start the vc —
   `start` alone would come back down disabled on the next reboot, silently
   leaving the validator dark:
   ```sh
   sudo systemctl start validator-init
   sudo systemctl enable --now lighthousevalidator
   ```
   Doppelganger protection delays signing by ~2 epochs (~13 min) — expected.
   Note: if the slashing-protection secret was deliberately left empty,
   validator-init logs a WARN and proceeds with doppelganger only — confirm
   that trade-off consciously before starting the vc.
7. Confirm the first attestation on beaconcha.in for validator `0xa177d5c2…ee7ec`.
8. Terminate the old instance. Update sibling stacks:
   `cd ../alarms && pulumi config set instanceId <new id> && pulumi up`;
   update the prometheus target IP in `../monitoring` and `pulumi up`.
9. Verify `reth node --help | grep -A2 minimal` on the box matches the
   `--minimal` flag used in `/opt/swannynode/scripts/start_reth.sh` (flag verified
   against the pinned release).

## Recovery runbook

| Failure | Action | Expected downtime |
|---|---|---|
| Instance dies (terminated / destroyed) | `pulumi refresh` **then** `pulumi up` — not automatic, see below | ~2-3 h+, needs a human |
| Instance stopped/started (same instance) | Nothing — self-heals on boot, see below | ~2 h instance-store; near-zero if EBS-backed |
| `/validator` volume corrupt | Restore latest DLM snapshot to a new volume in the AZ, `pulumi up`. **Do not** start the vc from a stale slashing-protection copy without first confirming no attestation was signed after the snapshot. | 1–2 h |
| AZ outage | `pulumi config set az us-east-2b`, restore the `/validator` volume from snapshot in the new AZ, `pulumi up`. New instance ID — same "vc comes up disabled" caveat as "Instance dies" below. | ~2 h |
| Total loss | Fresh `pulumi up` anywhere + secrets already in Secrets Manager + snapshot download. New instance ID — same "vc comes up disabled" caveat as "Instance dies" below. | ~6 h |

**"Instance dies" and "Instance stopped/started" are not the same failure and
`data-reprovision.service` does not make them equivalent — spelled out because
the difference matters at 2am:**

- **Instance dies** means the instance itself is gone — terminated, replaced,
  or hardware failure that AWS resolves by retiring the host. Pulumi's state
  file still says the old instance is running until you tell it otherwise:
  `pulumi up` alone diffs your config against **last-known state**, not
  live AWS state, so with no config changes it sees no diff and does
  **nothing**. Run `pulumi refresh` first so Pulumi notices the instance is
  gone, then `pulumi up` to create a replacement. That's a new instance ID, so
  the full bootstrap re-runs from scratch (mounts, users, binaries, units) —
  not just `data-reprovision.service`. The `/validator` volume and the EIP
  reattach automatically, so slashing protection is preserved. But bootstrap
  disables the vc on any host with no `.vc-first-boot-done` marker, which a
  genuinely new instance never has — **the vc comes up disabled**, deliberately.
  Before you run `systemctl enable --now lighthousevalidator` yourself, confirm
  the old instance cannot come back with its vc still enabled — e.g. AWS marked
  it impaired and stopped it, rather than actually destroying it, and someone
  starts it again later. If in doubt, terminate the old instance explicitly
  before enabling the vc on the new one. This step is manual by design; nothing
  here should auto-enable a validator client.

- **Instance stopped/started** means the same instance ID comes back — a
  deliberate `stop`/`start` (AWS maintenance, or an operator mistake), not a
  replacement. Pulumi is not involved at all: the instance ID hasn't changed,
  so the bootstrap command's triggers don't fire and it does not re-run.
  Instance-store `/data` contents **are** lost on a stop (a *reboot* keeps
  them — only a stop wipes the underlying NVMe), so `data-reprovision.service`
  — ordered before `reth-init.service`, ordinary boot, no pulumi — detects the
  empty/unmounted `/data`, remounts and reformats the instance-store NVMe,
  rebuilds the directory layout, and reinstalls the pinned client binaries,
  automatically. `reth-init` then sees no `mdbx.dat` and redoes the full
  snapshot download before the beacon node and vc have anything current to
  work from — that's the ~2 h. The `/validator` volume and the vc's systemd
  enabled/disabled state both live on durable storage the stop never touches,
  so **whatever the vc's state was before the stop is what it is after**: a
  live signer (already enabled) comes back and starts on its own once
  reth/beacon catch up (doppelganger adds ~13 min more); a pre-cutover host
  with the vc still disabled stays disabled, exactly as intended. EBS-backed
  stacks (`useInstanceStore false`) don't lose `/data` on a stop at all — no
  resync, just ordinary boot time.

**Resilience trade-off, stated plainly.** Moving chain data to instance store
raises instance-death recovery from ~15 min to ~2-3 h — and, per the table
above, that recovery is no longer a single automatic step: it needs a human to
run `pulumi refresh` and to judge it safe to re-enable the vc. That
reintroduces some of the exposure that drove the original move to EBS. This is
a deliberate trade: on
gp3 the node was averaging 3.93 s of block validation against a 4 s attestation
deadline and losing head votes on roughly 29% of blocks — *continuously*. An
occasional two-hour outage costs far less than a permanent ~29% haircut. If a
future failure makes that trade look wrong, `useInstanceStore false` reverts to
EBS-backed chain data without touching anything else.

Operational notes:
- `reth-init.service` retries the snapshot download up to 200 times (resumable);
  if it ever exhausts that or is SIGKILLed, restart it manually:
  `sudo systemctl restart reth-init` (reth.service starts automatically after it succeeds).
- `validator-init` writes `.validator-import-complete` under the validator
  datadir only after BOTH keystore and slashing imports succeed; delete that
  sentinel to force a re-import. The datadir is mode-dependent
  (`validatorDataDir()` in `types.go`): `/validator/lighthouse/.validator-import-complete`
  on instance-store stacks (the default), `/data/mainnet/lighthouse/.validator-import-complete`
  on EBS-backed stacks (`useInstanceStore false`).
- **Extended downtime (>5,000 blocks / ~17 hours behind):** reth's staged
  pipeline triggers a FULL state-root rebuild when the sync gap exceeds its
  merkle `clean_threshold` (default 5,000 blocks). On this gp3 volume that
  rebuild took ~4.5 hours (latency-bound at ~1,700 random 4KB reads/s) —
  observed during the 2026-07-19 recovery, where a 7,631-block gap cost:
  download 33m → extract 47m → verify 41m → trie rebuild ~4.5h → gap
  execution ~1h. If the node is more than ~5,000 blocks behind, it is FASTER
  to re-bootstrap from a fresh snapshot than to let the pipeline grind:
  `sudo systemctl stop reth && sudo rm -rf /data/mainnet/reth/* && sudo systemctl restart reth-init`
  (reth-init re-downloads ~170GB with resume and reth follows automatically;
  ~2h total). Alternatively, raise the threshold in the reth config
  (`[stages.merkle] clean_threshold`) before restarting reth so a medium gap
  uses the incremental merkle path instead.
- **EBS chain-data mode only** (`useInstanceStore false`) — none of this applies
  when chain data is on instance-store NVMe, which has no provisioned-IOPS
  ceiling to hit. A large *sync gap* state-root rebuild (see above) is
  latency-bound at queue-depth 1, not IOPS-bound — raising provisioned IOPS does
  not speed up that work; RAM/page-cache is what absorbs it. Steady-state
  validation is a different story: live `iostat` showed write IOPS at
  2,600–2,968 against the old 3,000 ceiling, with `%util` pinned at 92–100% and
  queue depth (`aqu-sz`) around 10 during MDBX write bursts — the volume was
  genuinely IOPS-bound, and read latency quadrupled (0.87ms → 3.55ms) when it
  happened, stalling reth's block validation. The chain-data volume is therefore
  provisioned at 6,000 IOPS / 156 MB/s (the `r8g.xlarge` EBS baseline ceiling).
  This was the interim mitigation; moving chain data to NVMe supersedes it,
  because the dominant cost was per-fault latency, not IOPS headroom.

**Never** run two copies of this stack (or the old host) against the same keys.
