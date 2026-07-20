package main

import (
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

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
		id, err := createIdentity(ctx)
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

		ctx.Export("instanceId", comp.Instance.ID())
		ctx.Export("publicIp", net.Eip.PublicIp)
		ctx.Export("dataVolumeId", sto.Volume.ID())
		return nil
	})
}
