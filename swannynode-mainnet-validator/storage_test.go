package main

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
)

func TestStorageVolumeShape(t *testing.T) {
	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := createStorage(ctx, testCfg())
		return err
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)

	vol := m.get("validator-data")
	require.NotNil(t, vol, "expected EBS volume resource 'validator-data'")
	require.Equal(t, 600.0, vol["size"].NumberValue())
	require.Equal(t, "gp3", vol["type"].StringValue())
	require.Equal(t, "us-east-2a", vol["availabilityZone"].StringValue())
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
