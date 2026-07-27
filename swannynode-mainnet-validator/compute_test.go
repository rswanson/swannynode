package main

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
)

func runFullInfra(t *testing.T) *recordingMocks { return runInfraWith(t, testCfg()) }

func runInfraWith(t *testing.T, cfg StackConfig) *recordingMocks {
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
		_, err = createCompute(ctx, cfg, net, sto, id)
		return err
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)
	return m
}

func TestComputeInstanceShape(t *testing.T) {
	m := runFullInfra(t)
	inst := m.get("validator")
	require.NotNil(t, inst, "expected EC2 instance 'validator'")
	require.Equal(t, "i8g.xlarge", inst["instanceType"].StringValue())
	require.Equal(t, "ami-mock", inst["ami"].StringValue(), "AMI must come from the SSM AL2023 arm64 parameter")
	root := inst["rootBlockDevice"].ObjectValue()
	require.Equal(t, 30.0, root["volumeSize"].NumberValue())
	require.Equal(t, "gp3", root["volumeType"].StringValue())
}

func TestComputeAttachmentAndEip(t *testing.T) {
	m := runInfraWith(t, ebsCfg())
	att := m.get("validator-data-attach")
	require.NotNil(t, att, "expected chain-data volume attachment on EBS stacks")
	require.Equal(t, "/dev/sdf", att["deviceName"].StringValue())
	require.NotNil(t, m.get("validator-eip-assoc"), "expected EIP association")
}

func TestComputeInstanceStoreHasNoChainAttachment(t *testing.T) {
	m := runFullInfra(t)
	require.Nil(t, m.get("validator-data-attach"),
		"instance-store stacks must not attach a chain-data EBS volume")
	// The validator-state volume is attached in BOTH modes: losing the
	// slashing-protection DB is the one unrecoverable failure.
	val := m.get("validator-state-attach")
	require.NotNil(t, val, "validator-state volume must always be attached")
	require.Equal(t, "/dev/sdg", val["deviceName"].StringValue())
}
