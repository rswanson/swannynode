package main

import (
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ssm"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type Compute struct {
	Instance *ec2.Instance
	// Attachment is the chain-data volume attachment; nil when chain data
	// lives on instance-store NVMe.
	Attachment          *ec2.VolumeAttachment
	ValidatorAttachment *ec2.VolumeAttachment
	EipAssoc            *ec2.EipAssociation
}

// createCompute provisions the disposable instance. AMI comes from Canonical's
// SSM public parameter for Ubuntu 24.04 arm64 — glibc 2.39 is required by the
// reth/lighthouse release binaries (AL2023's glibc 2.34 is too old for them).
// ignoreChanges on ami so routine AMI refreshes never force a replacement.
func createCompute(ctx *pulumi.Context, cfg StackConfig, net *Network, sto *Storage, id *Identity) (*Compute, error) {
	ami, err := ssm.LookupParameter(ctx, &ssm.LookupParameterArgs{
		Name: "/aws/service/canonical/ubuntu/server/24.04/stable/current/arm64/hvm/ebs-gp3/ami-id",
	})
	if err != nil {
		return nil, err
	}

	inst, err := ec2.NewInstance(ctx, "validator", &ec2.InstanceArgs{
		Ami:                 pulumi.String(ami.Value),
		InstanceType:        pulumi.String(cfg.InstanceType),
		SubnetId:            pulumi.String(net.SubnetId),
		VpcSecurityGroupIds: pulumi.StringArray{net.SecurityGroup.ID()},
		KeyName:             pulumi.String(cfg.KeyName),
		IamInstanceProfile:  id.Profile.Name,
		RootBlockDevice: &ec2.InstanceRootBlockDeviceArgs{
			VolumeSize: pulumi.Int(30),
			VolumeType: pulumi.String("gp3"),
		},
		Tags: pulumi.StringMap{"Name": pulumi.String("swannynode-mainnet-validator")},
	}, pulumi.IgnoreChanges([]string{"ami"}))
	if err != nil {
		return nil, err
	}

	var att *ec2.VolumeAttachment
	if sto.Volume != nil {
		att, err = ec2.NewVolumeAttachment(ctx, "validator-data-attach", &ec2.VolumeAttachmentArgs{
			DeviceName:                  pulumi.String("/dev/sdf"),
			InstanceId:                  inst.ID(),
			VolumeId:                    sto.Volume.ID(),
			StopInstanceBeforeDetaching: pulumi.Bool(true),
		})
		if err != nil {
			return nil, err
		}
	}

	var valAtt *ec2.VolumeAttachment
	if sto.ValidatorVolume != nil {
		valAtt, err = ec2.NewVolumeAttachment(ctx, "validator-state-attach", &ec2.VolumeAttachmentArgs{
			DeviceName:                  pulumi.String("/dev/sdg"),
			InstanceId:                  inst.ID(),
			VolumeId:                    sto.ValidatorVolume.ID(),
			StopInstanceBeforeDetaching: pulumi.Bool(true),
		})
		if err != nil {
			return nil, err
		}
	}

	assoc, err := ec2.NewEipAssociation(ctx, "validator-eip-assoc", &ec2.EipAssociationArgs{
		InstanceId:   inst.ID(),
		AllocationId: net.Eip.ID(),
	})
	if err != nil {
		return nil, err
	}

	return &Compute{
		Instance:            inst,
		Attachment:          att,
		ValidatorAttachment: valAtt,
		EipAssoc:            assoc,
	}, nil
}
