package main

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
)

// exportOutputs used to dereference sto.Volume unconditionally, which panics on
// instance-store stacks where there is no chain-data volume. Component tests
// never touched the export path, so `pulumi preview` was the first thing to hit
// it. These cover both storage modes.
func runExports(t *testing.T, cfg StackConfig) {
	t.Helper()
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
		exportOutputs(ctx, net, sto, comp)
		return nil
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", newMocks()))
	require.NoError(t, err)
}

func TestExportOutputsInstanceStore(t *testing.T) {
	require.NotPanics(t, func() { runExports(t, testCfg()) },
		"exports must not dereference the nil chain-data volume on instance-store stacks")
}

func TestExportOutputsEbs(t *testing.T) {
	require.NotPanics(t, func() { runExports(t, ebsCfg()) })
}
