package main

import (
	"github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// deployNode configures the instance over SSH: copies scripts/units, mounts
// the data volume, installs pinned client binaries, and enables services.
// The validator client is enabled but NOT started — validator-init gates it
// on secrets being present, and first start is a deliberate manual step in
// the migration runbook (after the old instance is stopped).
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

	copyScripts, err := remote.NewCopyToRemote(ctx, "copy-scripts", &remote.CopyToRemoteArgs{
		Connection: conn,
		Source:     pulumi.NewFileArchive("./scripts"),
		RemotePath: pulumi.String("/home/" + cfg.SshUser + "/deploy"),
		Triggers:   instanceTrigger,
	}, pulumi.DependsOn([]pulumi.Resource{comp.EipAssoc, comp.Attachment}))
	if err != nil {
		return err
	}

	copyUnits, err := remote.NewCopyToRemote(ctx, "copy-units", &remote.CopyToRemoteArgs{
		Connection: conn,
		Source:     pulumi.NewFileArchive("./config"),
		RemotePath: pulumi.String("/home/" + cfg.SshUser + "/deploy-units"),
		Triggers:   instanceTrigger,
	}, pulumi.DependsOn([]pulumi.Resource{comp.EipAssoc, comp.Attachment}))
	if err != nil {
		return err
	}

	bootstrapScript := pulumi.All(sto.Volume.ID().ToStringOutput()).ApplyT(func(vs []interface{}) string {
		volumeId := vs[0].(string)
		home := "/home/" + cfg.SshUser
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
# --- mount data volume ---
install -m 0755 ` + home + `/deploy/scripts/mount_data.sh /usr/local/sbin/mount_data.sh
VOLUME_ID=` + volumeId + ` /usr/local/sbin/mount_data.sh
# --- directory layout ---
mkdir -p /data/bin /data/scripts /data/shared /data/mainnet/reth /data/mainnet/lighthouse
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
chown root:eth /data/bin /data/scripts /data/shared
# --- validator env ---
mkdir -p /etc/swannynode
printf 'FEE_RECIPIENT=` + cfg.FeeRecipient + `\nSECRET_PREFIX=mainnet-validator\nAWS_DEFAULT_REGION=us-east-2\n' > /etc/swannynode/validator.env
chmod 600 /etc/swannynode/validator.env
# --- systemd units ---
install -m 0644 ` + home + `/deploy-units/config/*.service /etc/systemd/system/ 2>/dev/null || install -m 0644 ` + home + `/deploy-units/*.service /etc/systemd/system/
systemctl daemon-reload
# enable + start --no-block: reth-init is a oneshot with TimeoutStartSec=infinity,
# so a blocking start would hang this bootstrap for the whole snapshot download.
systemctl enable mevboost reth-init reth lighthousebeacon
systemctl start --no-block mevboost reth-init reth lighthousebeacon
systemctl enable validator-init lighthousevalidator
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
