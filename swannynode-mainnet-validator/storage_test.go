package main

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
)

func TestStorageVolumeShape(t *testing.T) {
	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := createStorage(ctx, ebsCfg())
		return err
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)

	vol := m.get("validator-data")
	require.NotNil(t, vol, "expected EBS volume resource 'validator-data'")
	require.Equal(t, 600.0, vol["size"].NumberValue())
	require.Equal(t, "gp3", vol["type"].StringValue())
	require.Equal(t, "us-east-2a", vol["availabilityZone"].StringValue())
	require.Equal(t, 6000.0, vol["iops"].NumberValue())
	require.Equal(t, 156.0, vol["throughput"].NumberValue())
	tags := vol["tags"].ObjectValue()
	require.Equal(t, "swannynode-mainnet-validator", tags["Backup"].StringValue())
}

func TestStorageDlmPolicy(t *testing.T) {
	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := createStorage(ctx, testCfg())
		return err
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)

	pol := m.get("validator-data-snapshots")
	require.NotNil(t, pol, "expected DLM lifecycle policy 'validator-data-snapshots'")
	details := pol["policyDetails"].ObjectValue()
	sched := details["schedules"].ArrayValue()[0].ObjectValue()
	require.Equal(t, 7.0, sched["retainRule"].ObjectValue()["count"].NumberValue())
	require.Equal(t, 24.0, sched["createRule"].ObjectValue()["interval"].NumberValue())
}

func TestStorageValidatorStateVolume(t *testing.T) {
	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := createStorage(ctx, testCfg())
		return err
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)

	vol := m.get("validator-state")
	require.NotNil(t, vol, "validator-state volume must exist in every storage mode")
	require.Equal(t, 20.0, vol["size"].NumberValue())
	require.Equal(t, "gp3", vol["type"].StringValue())
	// Backup-tagged so the DLM policy snapshots the slashing-protection DB.
	require.Equal(t, "swannynode-mainnet-validator",
		vol["tags"].ObjectValue()["Backup"].StringValue())

	// Regression: #80's 6,000 IOPS / 156 MB/s target the chain-data volume's
	// measured saturation. A careless merge lands them here instead, billing
	// ~$15/mo to over-provision a 20 GB disk that takes a couple of small
	// writes per epoch.
	require.False(t, vol["iops"].IsNumber(),
		"validator-state must stay at gp3 defaults, not inherit chain-data IOPS")
	require.False(t, vol["throughput"].IsNumber(),
		"validator-state must stay at gp3 defaults, not inherit chain-data throughput")
}

func TestStorageSkipsChainVolumeOnInstanceStore(t *testing.T) {
	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := createStorage(ctx, testCfg())
		return err
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)
	require.Nil(t, m.get("validator-data"),
		"instance-store stacks must not provision a chain-data EBS volume")
}
