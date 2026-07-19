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
| `volumeIops` | `3000` | raise if sync is IOPS-bound |
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

**Never** run two copies of this stack (or the old host) against the same keys.
