package main

import (
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
)

func TestDeployBootstrapContent(t *testing.T) {
	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		cfg := testCfg()
		net, err := createNetwork(ctx, cfg)
		if err != nil {
			return err
		}
		sto, err := createStorage(ctx, cfg)
		if err != nil {
			return err
		}
		id, err := createIdentity(ctx)
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
	script := boot["create"].StringValue()
	for _, want := range []string{
		"mount_data.sh",      // volume mounted before anything else
		"install_clients.sh", // pinned binaries
		"v2.4.1",             // reth pin flows into bootstrap
		"FEE_RECIPIENT=",     // env file for the vc
		"jwt.hex",            // shared JWT created
		"systemctl daemon-reload",
		"systemctl enable mevboost reth-init reth lighthousebeacon",
		// --no-block: reth-init is a oneshot with infinite timeout; a blocking
		// start would hang the bootstrap for the whole snapshot download
		"systemctl start --no-block mevboost reth-init reth lighthousebeacon",
		"awscli-exe-linux-aarch64.zip",                        // Ubuntu images lack aws cli; validator-init needs it
		"systemctl enable validator-init lighthousevalidator", // enabled, NOT started: gated on secrets + old node stopped
	} {
		require.True(t, strings.Contains(script, want), "bootstrap script missing %q", want)
	}
	require.False(t, strings.Contains(script, "enable --now"),
		"blocking enable --now must not be used; vc must never auto-start on first deploy")
	require.False(t, strings.Contains(script, "start --no-block lighthousevalidator"),
		"vc must never auto-start on first deploy")
}
