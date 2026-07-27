# swannynode-mainnet-validator

Cost-optimized mainnet Ethereum validator: reth `--minimal` + lighthouse bn/vc + mev-boost
on one Graviton EC2 instance, with a persistent (protected) EBS data volume, keys in
Secrets Manager, and daily DLM snapshots. Design spec:
`docs/specs/2026-07-19-mainnet-validator-design.md`.

## Configuration

| Key | Default | Notes |
|---|---|---|
| `aws:region` | — (set `us-east-2`) | |
| `az` | `us-east-2a` | change + snapshot-restore to move AZ |
| `instanceType` | `r8g.xlarge` | |
| `volumeSizeGb` | `600` | |
| `volumeIops` | `6000` | matched to the `r8g.xlarge` EBS baseline ceiling (6,000 IOPS) — going higher would be unusable spend |
| `volumeThroughput` | `156` | matched to the `r8g.xlarge` EBS baseline ceiling (156.25 MB/s) |
| `rethVersion` | `v2.4.1` | GitHub release tag |
| `lighthouseVersion` | `v8.2.0` | GitHub release tag |
| `mevboostVersion` | `1.12` | GitHub release tag (no `v` in asset name) |
| `feeRecipient` | **required** | your reward address; verify against a past proposal on beaconcha.in for validator `0xa177d5c2…ee7ec` |
| `keyName` | **required** | existing EC2 key pair name |
| `sshKey` | **required (secret)** | private key matching `keyName` |
| `sshUser` | `ec2-user` | |

## First deploy (migration from the failed host)

1. `pulumi preview` then `pulumi up`. The vc is enabled but will NOT start
   (validator-init fails fast until secrets exist — this is the safety gate).
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
6. Start the validator: `sudo systemctl start validator-init lighthousevalidator`.
   Doppelganger protection delays signing by ~2 epochs (~13 min) — expected.
   Note: if the slashing-protection secret was deliberately left empty,
   validator-init logs a WARN and proceeds with doppelganger only — confirm
   that trade-off consciously before starting the vc.
7. Confirm the first attestation on beaconcha.in for validator `0xa177d5c2…ee7ec`.
8. Terminate the old instance. Update sibling stacks:
   `cd ../alarms && pulumi config set instanceId <new id> && pulumi up`;
   update the prometheus target IP in `../monitoring` and `pulumi up`.
9. Verify `reth node --help | grep -A2 minimal` on the box matches the
   `--minimal` flag used in `/data/scripts/start_reth.sh` (flag verified
   against the pinned release).

## Recovery runbook

| Failure | Action | Expected downtime |
|---|---|---|
| Instance dies | `pulumi up` (new instance; volume + EIP reattach; secrets re-pulled automatically; vc: `systemctl start validator-init lighthousevalidator` after verifying the old instance is gone) | ~15 min |
| Data volume corrupt | Restore latest DLM snapshot to a new volume in the AZ, `pulumi import` or replace the volume resource, `pulumi up`; or delete data and let reth-init re-download | 1–6 h |
| AZ outage | `pulumi config set az us-east-2b`, restore volume from snapshot in new AZ, `pulumi up` | ~1 h |
| Total loss | Fresh `pulumi up` anywhere + secrets already in Secrets Manager + snapshot download | ~6 h |

Operational notes:
- `reth-init.service` retries the snapshot download up to 200 times (resumable);
  if it ever exhausts that or is SIGKILLed, restart it manually:
  `sudo systemctl restart reth-init` (reth.service starts automatically after it succeeds).
- `validator-init` writes `/data/mainnet/lighthouse/.validator-import-complete`
  only after BOTH keystore and slashing imports succeed; delete that sentinel to
  force a re-import.
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
- A large *sync gap* state-root rebuild (see above) is latency-bound at
  queue-depth 1, not IOPS-bound — raising provisioned IOPS does not speed up
  that work; RAM/page-cache (the reason for 32GB) is what absorbs it.
  Steady-state validation is a different story: live `iostat` showed write
  IOPS at 2,600–2,968 against the old 3,000 ceiling, with `%util` pinned at
  92–100% and queue depth (`aqu-sz`) around 10 during MDBX write bursts —
  the volume was genuinely IOPS-bound, and read latency quadrupled
  (0.87ms → 3.55ms) when it happened, stalling reth's block execution. The
  volume is now provisioned at 6,000 IOPS / 156 MB/s (the `r8g.xlarge`
  instance's EBS baseline ceiling) to give steady-state validation headroom.

**Never** run two copies of this stack (or the old host) against the same keys.
