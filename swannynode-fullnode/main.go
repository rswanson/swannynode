package main

import (
	"github.com/pulumi/pulumi-command/sdk/go/command/remote"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
	"github.com/rswanson/node_deployer/consensusClient"
	"github.com/rswanson/node_deployer/executionClient"
)

type DeploymentComponentArgs struct {
	Connection      *remote.ConnectionArgs
	Network         string
	DeploymentType  string
	ConsensusClient string
	ExecutionClient string
}

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg := config.New(ctx, "")
		connection := &remote.ConnectionArgs{
			PrivateKey: cfg.RequireSecret("sshKey"),
			User:       pulumi.String("root"),
			Host:       cfg.RequireSecret("host"),
			Port:       pulumi.Float64Ptr(22),
		}

		// install lighthouse and reth package deps
		installDeps, err := remote.NewCommand(ctx, "installDeps", &remote.CommandArgs{
			Connection: connection,
			Create:     pulumi.String("yum -y install git make perl clang cmake"),
		})
		if err != nil {
			ctx.Log.Error("Error installing dependencies", nil)
			return err
		}

		// Create the reth user
		rethUser, err := remote.NewCommand(ctx, "createUser-reth", &remote.CommandArgs{
			Connection: connection,
			Create:     pulumi.String("useradd -m -s /bin/bash reth"),
			Delete:     pulumi.String("userdel -r reth"),
		})
		if err != nil {
			ctx.Log.Error("Error creating reth user", nil)
			return err
		}

		// Create the lighthouse user
		lighthouseUser, err := remote.NewCommand(ctx, "createUser-lighthouse", &remote.CommandArgs{
			Connection: connection,
			Create:     pulumi.String("useradd -m -s /bin/bash lighthouse"),
		})
		if err != nil {
			ctx.Log.Error("Error creating lighthouse user", nil)
			return err
		}

		// Create eth group
		ethGroup, err := remote.NewCommand(ctx, "createEthGroup", &remote.CommandArgs{
			Connection: connection,
			Create:     pulumi.String("groupadd eth"),
		})
		if err != nil {
			ctx.Log.Error("Error creating eth group", nil)
			return err
		}

		// Add reth user to eth group
		groupAddReth, err := remote.NewCommand(ctx, "addRethUserToEthGroup", &remote.CommandArgs{
			Connection: connection,
			Create:     pulumi.String("usermod -aG eth reth"),
		}, pulumi.DependsOn([]pulumi.Resource{rethUser, ethGroup}))
		if err != nil {
			ctx.Log.Error("Error adding reth user to eth group", nil)
			return err
		}

		// create the data directory structure
		dataDir, err := remote.NewCommand(ctx, "createDataDir", &remote.CommandArgs{
			Connection: connection,
			Create:     pulumi.String("mkdir -p /data/repos/mainnet/ /data/scripts/ /data/shared/ /data/bin/"),
		})
		if err != nil {
			ctx.Log.Error("Error creating data directory", nil)
			return err
		}

		// Add lighthouse user to eth group
		groupAddLighthouse, err := remote.NewCommand(ctx, "addLighthouseUserToEthGroup", &remote.CommandArgs{
			Connection: connection,
			Create:     pulumi.String("usermod -aG eth lighthouse"),
		}, pulumi.DependsOn([]pulumi.Resource{lighthouseUser, ethGroup}))
		if err != nil {
			ctx.Log.Error("Error adding lighthouse user to eth group", nil)
			return err
		}

		_, err = consensusClient.NewConsensusClientComponent(ctx, "consensusClient", &consensusClient.ConsensusClientComponentArgs{
			Client:         "lighthouse",
			Network:        "mainnet",
			DeploymentType: "source",
			DataDir:        "/data/mainnet/lighthouse",
			Connection:     connection,
		}, pulumi.DependsOn([]pulumi.Resource{groupAddLighthouse, dataDir, installDeps}))
		if err != nil {
			ctx.Log.Error("Error creating consensus client", nil)
			return err
		}

		// Create execution client
		_, err = executionClient.NewExecutionClientComponent(ctx, "executionClient", &executionClient.ExecutionClientComponentArgs{
			Client:         "reth",
			Network:        "mainnet",
			DeploymentType: "source",
			DataDir:        "/data/mainnet/reth",
			Connection:     connection,
		}, pulumi.DependsOn([]pulumi.Resource{groupAddReth, dataDir, installDeps}))
		if err != nil {
			ctx.Log.Error("Error creating execution client", nil)
			return err
		}

		return nil
	})
}
