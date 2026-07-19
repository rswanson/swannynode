# Mainnet Validator Stack — Design Spec (DRAFT)

**Date:** 2026-07-19
**Status:** Draft — under review
**Author:** Swanny (with Claude)

## Background

The mainnet validator (`0xa177d5c28a60469bd6fe28255cc510f92ce9efd94bff28e49af97f0f84dd6b69a1262cc0fda6a10d672524f0735ee7ec`) previously ran on a manually provisioned EC2 instance (`3.145.206.252`, us-east-2) with all chain data on 3.5TB of direct-attached instance-store NVMe. An AWS hardware failure destroyed the instance store; the replacement disk came up raw and the validator has been offline since. The instance was never captured as infrastructure-as-code, so neither the hardware setup nor the storage architecture was reproducible.

What survived (on the root EBS volume): the lighthouse validator keystore, keystore password (`validator_definitions.yml`), and the slashing-protection database. These are backed up locally at `~/validator-backup-2026-07-19/lighthouse-mainnet-validators.tar.gz`. Only chain data (fully re-syncable) was lost.

## Goals

1. Rebuild the mainnet validator as a fully Pulumi-managed stack — infra and client configuration.
2. Maximally cost-optimize using reth 2.0 `--minimal` mode (sub-300GB execution data vs 1.2TB+ full node).
3. Survive instance/hardware failure with minimal downtime and zero manual intervention where possible.
4. Make every recovery path (instance loss, volume loss, AZ loss, total loss) self-initializing and documented.

## Non-goals

- Multi-validator / large-scale key management.
- High availability via redundant active nodes (single validator; doppelganger risk makes active-active undesirable).
- Migrating the existing holesky EKS stack or fullnode stack.

## Architecture

New Pulumi Go project `swannynode-mainnet-validator/` (added to `go.work`), stack `mainnet`, region `us-east-2`, AZ as stack config (default `us-east-2a`).

```
┌─ Pulumi stack: mainnet-validator ──────────────────────────┐
│  EC2 r8g.xlarge (Graviton4, AL2023 arm64)                  │
│  ├─ root: 30GB gp3 (OS only, disposable)                   │
│  ├─ EBS data vol: 600GB gp3 (separate resource, /data)     │
│  │   ├─ reth --minimal  (~300GB)                           │
│  │   ├─ lighthouse bn   (~150GB, checkpoint sync)          │
│  │   └─ lighthouse vc   (keys + slashing DB)               │
│  ├─ mev-boost (flashbots, ultrasound, aestus)              │
│  ├─ Elastic IP, security group (p2p + SSH), IAM role       │
│  └─ SSM agent enabled (console access even if SSH breaks)  │
│  Secrets Manager: keystore / password / slashing export    │
│  DLM: daily EBS snapshot of data volume, retain 7          │
└────────────────────────────────────────────────────────────┘
```

### Compute

- **r8g.xlarge** (Graviton4, 4 vCPU / 32GB RAM), Amazon Linux 2023 arm64 AMI.
- 32GB RAM is deliberate: reth on EBS leans on the OS page cache to absorb read IOPS, making gp3 sufficient for validation without local NVMe.
- ~$172/mo on-demand in us-east-2; ~$105/mo with a 1-yr no-upfront compute savings plan (savings plan purchase is out of band, not in the stack).
- Instance is disposable: nothing irreplaceable lives on the root volume.

### Storage

- **Data volume:** 600GB gp3 as a standalone `aws.ebs.Volume` resource, `retainOnDelete: true` and `protect` enabled. Attached at `/dev/sdf`, mounted at `/data` by filesystem UUID.
- gp3 baseline 3000 IOPS to start; IOPS is a stack config value so it can be raised (~6-8k, ~$15-25/mo) without replacement if sync/steady-state monitoring shows pressure.
- Formatted ext4 **only if blank**; a reattached volume is never touched.
- **DLM lifecycle policy:** daily snapshot of the data volume (by tag), retain 7.

### Network

- Elastic IP (stable peer identity / DNS target).
- Security group ingress: reth p2p 30303 tcp+udp, lighthouse p2p 9000 tcp+udp (+ QUIC udp port), SSH 22. Metrics ports are not exposed publicly.
- SSM Session Manager enabled as a second access path independent of SSH.

### Identity & secrets

- IAM instance role: read access to exactly three Secrets Manager secrets + SSM managed-instance core policy. Nothing else.
- Secrets (created **empty** by Pulumi; values pushed once out-of-band via `aws secretsmanager put-secret-value` from the local backup, so key material never enters Pulumi state):
  1. `mainnet-validator/keystore` — voting keystore JSON
  2. `mainnet-validator/keystore-password` — keystore password
  3. `mainnet-validator/slashing-protection` — slashing protection interchange export
- Bootstrap script on the instance pulls all three via the instance role before the vc first starts.

### Client configuration layer

Follows the `swannynode-fullnode` pattern: `remote.Command` over SSH for bootstrap, systemd units + `start_*.sh` scripts checked into `config/` and `scripts/` in the project dir.

Deviation from `swannynode-fullnode`: clients installed from **pinned official release binaries (arm64 tarballs)**, not source builds — 4 vCPUs make source builds slow, and pinned releases are reproducible. If `node_deployer` supports a binary deployment type, use it; otherwise plain `remote.Command` download+verify steps. (Open item: confirm `node_deployer` capabilities during implementation.)

Bootstrap sequence:

1. **Mount first.** Format data volume ext4 only if blank; mount `/data` by UUID via fstab. All service units carry `RequiresMountsFor=/data` so clients can never start against an unmounted disk (prevents silently initializing a fresh datadir on the root volume).
2. **Users/layout.** `reth`, `lighthouse`, `mevboost` users under `eth` group; `/data/mainnet/{reth,lighthouse}`; JWT at `/data/shared/jwt.hex`.
3. **Install pinned client binaries.**
4. **Snapshot init (explicit step — see below).**
5. **Services up in order; vc last, guarded.**

### Snapshot initialization: `reth-init.service`

Oneshot, idempotent systemd unit; `reth.service` has `Requires=reth-init.service` + `After=reth-init.service`; both gated on `RequiresMountsFor=/data`.

- **Guard:** if the reth datadir is non-empty (`db/mdbx.dat` exists under `/data/mainnet/reth`), exit 0 immediately — a healthy or reattached datadir is never touched.
- **If empty:** run `reth download --minimal -y --chain mainnet --datadir /data/mainnet/reth` (~170GB from snapshots.reth.rs; parallel streams, HTTP-Range resume). `Restart=on-failure` on the unit means a mid-download crash resumes, not restarts.
- Result: every recovery path (fresh deploy, volume replacement, AZ move) converges on "empty datadir → download → start" with zero manual steps.
- Exact flag spelling pinned against the deployed reth release during implementation.

Lighthouse bn equivalently self-initializes via `--checkpoint-sync-url` when its datadir is empty.

### Services (systemd, `Restart=always`, journald)

| Unit | Role | Notes |
|---|---|---|
| `reth-init.service` | oneshot snapshot download | idempotent guard on datadir |
| `reth.service` | execution, `reth node --minimal` | authrpc 8551, metrics 9001 |
| `lighthousebeacon.service` | consensus bn | checkpoint sync, metrics 6064, builder → mev-boost |
| `lighthousevalidator.service` | vc | doppelganger protection ON, metrics 6065 |
| `mevboost.service` | relays | `-mainnet -min-bid 0.05`, relays: flashbots, ultrasound, aestus (Eden removed — defunct) |

Metrics ports match existing repo conventions so current Grafana dashboards (`reth-overview`, `lighthouse-overview`, `validator-overview`) work unchanged. The `monitoring` stack's Prometheus scrape target and the `alarms` stack's `instanceId` get updated to the new host as config changes.

### Anti-slashing guardrails

- Doppelganger detection enabled on the vc (waits ~2 epochs before signing).
- Slashing-protection DB imported from Secrets Manager before the vc's first start, always migrates with the keys.
- Old instance stopped before the new vc is enabled (it is not currently validating — no lighthouse services survived its reboot — but stop it anyway as a hard guarantee).
- Never run two instances of the stack against the same keys.

## Failure & recovery matrix

| Failure | Recovery | Downtime |
|---|---|---|
| Instance/hardware dies | `pulumi up` → new instance; volume reattaches; secrets re-pulled | ~15 min |
| Data volume corrupted | Restore DLM snapshot, or delete → `reth-init` snapshot download + checkpoint sync | 1–6 h |
| AZ outage | Change AZ config; restore volume from snapshot in new AZ; `pulumi up` | ~1 h |
| Total loss (incl. laptop) | Keys from Secrets Manager; infra from this repo; chain from public snapshots | ~6 h |

## Cost

| Item | Monthly (on-demand) |
|---|---|
| r8g.xlarge | ~$172 |
| 600GB gp3 (+ optional IOPS bump) | ~$48 (+$15–25) |
| EIP + DLM snapshots + Secrets Manager | ~$15 |
| **Total** | **~$250 (~$185 w/ 1-yr savings plan)** |

vs. ~$530–1000/mo for the old i-family instance-store machine.

## Migration plan

1. Deploy stack (`pulumi preview` then `pulumi up`).
2. Push the three secret values from the local backup.
3. Watch reth snapshot download + sync; verify bn health.
4. Stop the old instance.
5. Enable the vc; confirm slashing DB import + doppelganger pass.
6. Confirm first attestation for `0xa177d5c2…` on beaconcha.in.
7. Terminate the old instance; update `alarms` + `monitoring` stack config to the new host.

## Testing

- `pulumi preview` before every apply.
- Staged bring-up as in the migration plan (bn healthy before vc enabled).
- Idempotency check: re-run `pulumi up` after first success — no diff, no service churn.
- Recovery drill (post-migration, optional but recommended): terminate the instance, `pulumi up`, verify the volume reattaches and attestations resume without manual steps.

## Open items (resolved during implementation)

1. Exact reth CLI flags for `--minimal` / `download` against the pinned release version.
2. Whether `node_deployer` supports binary installs, or plain `remote.Command` is used.
3. Fee-recipient address — recover from old host bash history or provided by Swanny.
4. Whether to keep tailscale on the new host (old box ran tailscaled; not required by this design).

## Follow-ups (out of scope)

- Remove committed private key `swannynode-fullnode/key.pem` from the repo.
- Decide on savings-plan purchase after the stack proves stable.
