package main

import (
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/dlm"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ebs"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const backupTag = "swannynode-mainnet-validator"

type Storage struct {
	Volume *ebs.Volume
}

// createStorage provisions the persistent chain-data volume and its snapshot
// policy. The volume is Protect()ed and RetainOnDelete so no Pulumi operation
// can destroy chain data.
func createStorage(ctx *pulumi.Context, cfg StackConfig) (*Storage, error) {
	vol, err := ebs.NewVolume(ctx, "validator-data", &ebs.VolumeArgs{
		AvailabilityZone: pulumi.String(cfg.Az),
		Size:             pulumi.Int(cfg.VolumeSizeGb),
		Type:             pulumi.String("gp3"),
		Iops:             pulumi.Int(cfg.VolumeIops),
		Throughput:       pulumi.Int(cfg.VolumeThroughput),
		Tags: pulumi.StringMap{
			"Name":   pulumi.String("swannynode-mainnet-validator-data"),
			"Backup": pulumi.String(backupTag),
		},
	}, pulumi.Protect(true), pulumi.RetainOnDelete(true))
	if err != nil {
		return nil, err
	}

	dlmRole, err := iam.NewRole(ctx, "dlm-lifecycle-role", &iam.RoleArgs{
		AssumeRolePolicy: pulumi.String(`{
			"Version": "2012-10-17",
			"Statement": [{
				"Effect": "Allow",
				"Principal": {"Service": "dlm.amazonaws.com"},
				"Action": "sts:AssumeRole"
			}]
		}`),
	})
	if err != nil {
		return nil, err
	}
	_, err = iam.NewRolePolicyAttachment(ctx, "dlm-lifecycle-attach", &iam.RolePolicyAttachmentArgs{
		Role:      dlmRole.Name,
		PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AWSDataLifecycleManagerServiceRole"),
	})
	if err != nil {
		return nil, err
	}

	_, err = dlm.NewLifecyclePolicy(ctx, "validator-data-snapshots", &dlm.LifecyclePolicyArgs{
		Description:      pulumi.String("Daily snapshots of the mainnet validator data volume"),
		ExecutionRoleArn: dlmRole.Arn,
		State:            pulumi.String("ENABLED"),
		PolicyDetails: &dlm.LifecyclePolicyPolicyDetailsArgs{
			ResourceTypes: pulumi.StringArray{pulumi.String("VOLUME")},
			TargetTags:    pulumi.StringMap{"Backup": pulumi.String(backupTag)},
			Schedules: dlm.LifecyclePolicyPolicyDetailsScheduleArray{
				&dlm.LifecyclePolicyPolicyDetailsScheduleArgs{
					Name: pulumi.String("daily"),
					CreateRule: &dlm.LifecyclePolicyPolicyDetailsScheduleCreateRuleArgs{
						Interval:     pulumi.Int(24),
						IntervalUnit: pulumi.String("HOURS"),
						Times:        pulumi.String("09:00"),
					},
					RetainRule: &dlm.LifecyclePolicyPolicyDetailsScheduleRetainRuleArgs{
						Count: pulumi.Int(7),
					},
					CopyTags: pulumi.Bool(true),
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	return &Storage{Volume: vol}, nil
}
