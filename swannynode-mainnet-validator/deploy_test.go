package main

import (
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
)

// bootstrapFor renders the remote bootstrap script for a given stack config.
func bootstrapFor(t *testing.T, cfg StackConfig) string {
	t.Helper()
	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		net, err := createNetwork(ctx, cfg)
		if err != nil {
			return err
		}
		sto, err := createStorage(ctx, cfg)
		if err != nil {
			return err
		}
		id, err := createIdentity(ctx, cfg)
		if err != nil {
			return err
		}
		comp, err := createCompute(ctx, cfg, net, sto, id)
		if err != nil {
			return err
		}
		return deployNode(ctx, cfg, net, sto, comp, pulumi.String("fake-ssh-key").ToStringOutput())
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)

	boot := m.get("bootstrap")
	require.NotNil(t, boot, "expected remote command 'bootstrap'")
	return boot["create"].StringValue()
}

func TestDeployBootstrapContent(t *testing.T) {
	script := bootstrapFor(t, testCfg())
	for _, want := range []string{
		"install_clients.sh", // pinned binaries
		"v2.4.1",             // reth pin flows into bootstrap
		"FEE_RECIPIENT=",     // env file for the vc
		"jwt.hex",            // shared JWT created
		"systemctl daemon-reload",
		"systemctl enable mevboost reth-init reth lighthousebeacon",
		// --no-block: reth-init is a oneshot with infinite timeout; a blocking
		// start would hang the bootstrap for the whole snapshot download
		"systemctl start --no-block mevboost reth-init reth lighthousebeacon",
		"awscli-exe-linux-aarch64.zip",            // Ubuntu images lack aws cli; validator-init needs it
		"systemctl enable validator-init",         // pre-stage keys so cutover is a fast stop/start
		"MOUNT=/validator",                        // slashing protection on its own EBS volume
		"VALIDATOR_DATADIR=/validator/lighthouse", // vc + validator-init both read this
		"systemctl disable lighthousevalidator",
	} {
		require.True(t, strings.Contains(script, want), "bootstrap script missing %q", want)
	}
	require.False(t, strings.Contains(script, "enable --now"),
		"blocking enable --now must not be used; vc must never auto-start on first deploy")
	require.False(t, strings.Contains(script, "start --no-block lighthousevalidator"),
		"vc must never auto-start on first deploy")

	// Regression: this host can be built while ANOTHER host is still signing for
	// the same keys. An enabled vc unit would start a second signer on reboot
	// and get the validator slashed, so it must be left explicitly disabled.
	require.False(t, strings.Contains(script, "systemctl enable validator-init lighthousevalidator"),
		"vc must NOT be enabled at bootstrap: a reboot during a parallel migration would start a second signer")
}

func TestDeployMountsInstanceStoreWhenConfigured(t *testing.T) {
	script := bootstrapFor(t, testCfg())
	require.True(t, strings.Contains(script, "mount_instance_store.sh"),
		"instance-store stacks must mount /data from ephemeral NVMe")
	require.False(t, strings.Contains(script, "MOUNT=/data /usr/local/sbin/mount_data.sh"),
		"instance-store stacks must not mount /data from EBS")
}

func TestDeployMountsEbsChainDataWhenConfigured(t *testing.T) {
	script := bootstrapFor(t, ebsCfg())
	require.True(t, strings.Contains(script, "MOUNT=/data /usr/local/sbin/mount_data.sh"),
		"EBS stacks must mount /data from the chain-data volume")
	require.False(t, strings.Contains(script, "mount_instance_store.sh"),
		"EBS stacks must not touch the instance-store mount path")
}

// Regression for the hazard `pulumi preview --stack mainnet` exposed: an
// unconditional /validator datadir repoints a LIVE validator at an empty
// volume. It keeps attesting off in-memory state, then on the next restart
// validator-init re-imports a stale interchange from Secrets Manager and signs
// against a slashing DB missing everything since the export.
func TestDeployEbsStackKeepsValidatorDataDirInPlace(t *testing.T) {
	script := bootstrapFor(t, ebsCfg())
	require.True(t, strings.Contains(script, "VALIDATOR_DATADIR=/data/mainnet/lighthouse"),
		"EBS stacks must keep the historical validator datadir")
	require.False(t, strings.Contains(script, "/validator/lighthouse"),
		"EBS stacks must never reference the instance-store validator path")
	require.False(t, strings.Contains(script, "MOUNT=/validator"),
		"EBS stacks must not provision or mount a separate validator volume")
}

func TestDeployInstanceStoreSplitsValidatorDataDir(t *testing.T) {
	script := bootstrapFor(t, testCfg())
	require.True(t, strings.Contains(script, "VALIDATOR_DATADIR=/validator/lighthouse"),
		"instance-store stacks must keep validator state off ephemeral /data")
	require.True(t, strings.Contains(script, "MOUNT=/validator"),
		"instance-store stacks must mount the validator volume")
}
