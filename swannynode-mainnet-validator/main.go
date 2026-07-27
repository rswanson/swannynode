package main

import (
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

// exportOutputs publishes the stack outputs. Split out of main so the nil
// chain-data volume case (instance-store stacks) is unit-testable — a plain
// sto.Volume.ID() here panicked during `pulumi preview`, and no amount of
// component-level testing caught it because nothing else touches the exports.
func exportOutputs(ctx *pulumi.Context, net *Network, sto *Storage, comp *Compute) {
	ctx.Export("instanceId", comp.Instance.ID())
	ctx.Export("publicIp", net.Eip.PublicIp)
	// Each volume is absent in one storage mode; export only what exists.
	if sto.ValidatorVolume != nil {
		ctx.Export("validatorVolumeId", sto.ValidatorVolume.ID())
	}
	if sto.Volume != nil {
		ctx.Export("dataVolumeId", sto.Volume.ID())
	}
}

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg := config.New(ctx, "")
		sc := loadStackConfig(cfg)

		net, err := createNetwork(ctx, sc)
		if err != nil {
			return err
		}
		sto, err := createStorage(ctx, sc)
		if err != nil {
			return err
		}
		id, err := createIdentity(ctx, sc)
		if err != nil {
			return err
		}
		comp, err := createCompute(ctx, sc, net, sto, id)
		if err != nil {
			return err
		}

		sshKey := cfg.RequireSecret("sshKey")
		if err := deployNode(ctx, sc, net, sto, comp, sshKey); err != nil {
			return err
		}

		exportOutputs(ctx, net, sto, comp)
		return nil
	})
}
