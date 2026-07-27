package main

import (
	"github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// deployNode configures the instance over SSH: copies scripts/units, mounts
// /data (ephemeral NVMe or EBS) and /validator (always EBS), installs pinned
// client binaries, and enables services.
//
// The validator client is left DISABLED, not merely stopped. This host may be
// built while another host is still signing for the same keys, so an enabled
// unit would start a second signer on reboot. scripts/cutover_validator.sh
// enables it, and only after proving the previous signer is dead.
func deployNode(ctx *pulumi.Context, cfg StackConfig, net *Network, sto *Storage, comp *Compute, sshKey pulumi.StringOutput) error {
	conn := &remote.ConnectionArgs{
		Host:       net.Eip.PublicIp,
		User:       pulumi.String(cfg.SshUser),
		PrivateKey: sshKey,
		Port:       pulumi.Float64Ptr(22),
	}

	// The SSH host is the stable EIP, so a replacement instance looks identical
	// to the connection. Trigger on the instance ID so every new instance gets
	// fresh copies and a fresh bootstrap run.
	instanceTrigger := pulumi.Array{comp.Instance.ID()}

	// Either attachment can be nil depending on storage mode, and a nil in a
	// DependsOn slice panics inside the SDK rather than being ignored.
	deps := []pulumi.Resource{comp.EipAssoc}
	if comp.Attachment != nil {
		deps = append(deps, comp.Attachment)
	}
	if comp.ValidatorAttachment != nil {
		deps = append(deps, comp.ValidatorAttachment)
	}

	copyScripts, err := remote.NewCopyToRemote(ctx, "copy-scripts", &remote.CopyToRemoteArgs{
		Connection: conn,
		Source:     pulumi.NewFileArchive("./scripts"),
		RemotePath: pulumi.String("/home/" + cfg.SshUser + "/deploy"),
		Triggers:   instanceTrigger,
	}, pulumi.DependsOn(deps))
	if err != nil {
		return err
	}

	copyUnits, err := remote.NewCopyToRemote(ctx, "copy-units", &remote.CopyToRemoteArgs{
		Connection: conn,
		Source:     pulumi.NewFileArchive("./config"),
		RemotePath: pulumi.String("/home/" + cfg.SshUser + "/deploy-units"),
		Triggers:   instanceTrigger,
	}, pulumi.DependsOn(deps))
	if err != nil {
		return err
	}

	// Either volume may be absent depending on storage mode; feed placeholders
	// through the Apply so the shape stays uniform either way.
	chainVolID := pulumi.String("").ToStringOutput()
	if sto.Volume != nil {
		chainVolID = sto.Volume.ID().ToStringOutput()
	}
	valVolID := pulumi.String("").ToStringOutput()
	if sto.ValidatorVolume != nil {
		valVolID = sto.ValidatorVolume.ID().ToStringOutput()
	}

	bootstrapScript := pulumi.All(chainVolID, valVolID).ApplyT(func(vs []interface{}) string {
		volumeId := vs[0].(string)
		validatorVolumeId := vs[1].(string)
		home := "/home/" + cfg.SshUser
		validatorDataDir := cfg.validatorDataDir()

		// /data is chain data: ephemeral NVMe or EBS depending on config.
		mountChainData := `install -m 0755 ` + home + `/deploy/scripts/mount_instance_store.sh /usr/local/sbin/mount_instance_store.sh
MOUNT=/data /usr/local/sbin/mount_instance_store.sh`
		if !cfg.UseInstanceStore {
			mountChainData = `install -m 0755 ` + home + `/deploy/scripts/mount_data.sh /usr/local/sbin/mount_data.sh
VOLUME_ID=` + volumeId + ` MOUNT=/data /usr/local/sbin/mount_data.sh`
		}

		// Only instance-store stacks get a separate validator volume. On EBS
		// stacks validator state stays where it already is under /data.
		mountValidator := "# validator state lives under /data on EBS-backed stacks"
		if cfg.UseInstanceStore {
			mountValidator = `install -m 0755 ` + home + `/deploy/scripts/mount_data.sh /usr/local/sbin/mount_data.sh
VOLUME_ID=` + validatorVolumeId + ` MOUNT=/validator /usr/local/sbin/mount_data.sh`
		}

		return `set -euo pipefail
sudo bash -s <<'BOOTSTRAP'
set -euo pipefail
# --- aws cli (Ubuntu images do not ship it; needed by validator-init) ---
if ! command -v aws >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq && apt-get install -y -qq unzip >/dev/null
  curl -fsSL -o /tmp/awscliv2.zip https://awscli.amazonaws.com/awscli-exe-linux-aarch64.zip
  unzip -q /tmp/awscliv2.zip -d /tmp && /tmp/aws/install && rm -rf /tmp/aws /tmp/awscliv2.zip
fi
# --- users & groups (idempotent) ---
getent group eth >/dev/null || groupadd eth
for u in reth lighthouse mevboost; do
  id "$u" >/dev/null 2>&1 || useradd -m -s /bin/bash -g eth "$u"
done
# --- mount chain data at /data (ephemeral NVMe or EBS, per stack config) ---
` + mountChainData + `
# --- validator state (own EBS volume only when /data is ephemeral) ---
` + mountValidator + `
# --- directory layout ---
mkdir -p /data/bin /data/scripts /data/shared /data/mainnet/reth /data/mainnet/lighthouse
mkdir -p ` + validatorDataDir + `
# --- scripts ---
install -m 0755 ` + home + `/deploy/scripts/*.sh /data/scripts/
# --- shared JWT (created once) ---
[ -f /data/shared/jwt.hex ] || (umask 077 && openssl rand -hex 32 > /data/shared/jwt.hex)
chgrp eth /data/shared/jwt.hex && chmod 640 /data/shared/jwt.hex
# --- client binaries (pinned) ---
RETH_VERSION=` + cfg.RethVersion + ` LIGHTHOUSE_VERSION=` + cfg.LighthouseVersion + ` MEVBOOST_VERSION=` + cfg.MevboostVersion + ` /data/scripts/install_clients.sh
# --- ownership ---
chown -R reth:eth /data/mainnet/reth
chown -R lighthouse:eth /data/mainnet/lighthouse
chown -R lighthouse:eth ` + validatorDataDir + `
chown root:eth /data/bin /data/scripts /data/shared
# --- validator env ---
mkdir -p /etc/swannynode
printf 'FEE_RECIPIENT=` + cfg.FeeRecipient + `\nSECRET_PREFIX=mainnet-validator\nAWS_DEFAULT_REGION=us-east-2\nVALIDATOR_DATADIR=` + validatorDataDir + `\n' > /etc/swannynode/validator.env
chmod 600 /etc/swannynode/validator.env
# --- systemd units ---
install -m 0644 ` + home + `/deploy-units/config/*.service /etc/systemd/system/ 2>/dev/null || install -m 0644 ` + home + `/deploy-units/*.service /etc/systemd/system/
systemctl daemon-reload
# enable + start --no-block: reth-init is a oneshot with TimeoutStartSec=infinity,
# so a blocking start would hang this bootstrap for the whole snapshot download.
systemctl enable mevboost reth-init reth lighthousebeacon
systemctl start --no-block mevboost reth-init reth lighthousebeacon
# validator-init pre-stages keystore + slashing protection so the cutover window
# is only a stop/export/import/start, not a Secrets Manager round trip.
systemctl enable validator-init
systemctl start --no-block validator-init
# The validator client is deliberately left DISABLED. This host may be built
# while ANOTHER host is still signing for the same keys; if the VC were enabled,
# a reboot here would silently start a second signer and get the validator
# slashed. cutover_validator.sh enables it, and only after proving the old
# signer is dead.
systemctl disable lighthousevalidator >/dev/null 2>&1 || true
BOOTSTRAP
echo bootstrap-complete`
	}).(pulumi.StringOutput)

	_, err = remote.NewCommand(ctx, "bootstrap", &remote.CommandArgs{
		Connection: conn,
		Create:     bootstrapScript,
		Triggers: pulumi.Array{
			pulumi.String(cfg.RethVersion),
			pulumi.String(cfg.LighthouseVersion),
			pulumi.String(cfg.MevboostVersion),
			pulumi.String(cfg.FeeRecipient),
		},
	}, pulumi.DependsOn([]pulumi.Resource{copyScripts, copyUnits}))
	return err
}
