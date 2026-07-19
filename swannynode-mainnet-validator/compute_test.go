package main

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
)

func runFullInfra(t *testing.T) *recordingMocks {
	t.Helper()
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
	require.Equal(t, "r8g.xlarge", inst["instanceType"].StringValue())
	require.Equal(t, "ami-mock", inst["ami"].StringValue(), "AMI must come from the SSM AL2023 arm64 parameter")
	root := inst["rootBlockDevice"].ObjectValue()
	require.Equal(t, 30.0, root["volumeSize"].NumberValue())
	require.Equal(t, "gp3", root["volumeType"].StringValue())
}

func TestComputeAttachmentAndEip(t *testing.T) {
	m := runFullInfra(t)
	att := m.get("validator-data-attach")
	require.NotNil(t, att, "expected volume attachment")
	require.Equal(t, "/dev/sdf", att["deviceName"].StringValue())
	require.NotNil(t, m.get("validator-eip-assoc"), "expected EIP association")
}
