package main

import (
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/dlm"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ebs"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const backupTag = "swannynode-mainnet-validator"

type Storage struct {
	// Volume is the EBS chain-data volume. It is nil when the stack runs chain
	// data on ephemeral instance-store NVMe.
	Volume *ebs.Volume
	// ValidatorVolume holds the slashing-protection DB and keystores. Always
	// EBS, always present — losing it risks a slashing event, so it is the one
	// piece of host state that must survive instance replacement.
	ValidatorVolume *ebs.Volume
}

// createStorage provisions the validator-state volume (always) and the
// chain-data volume (only when not using instance store), plus the shared
// snapshot policy. Both volumes are Protect()ed and RetainOnDelete so no
// Pulumi operation can destroy them.
func createStorage(ctx *pulumi.Context, cfg StackConfig) (*Storage, error) {
	// Only needed when /data is ephemeral. On EBS-backed stacks validator state
	// already sits on a durable volume, and introducing this one would repoint a
	// live validator at an empty datadir.
	var valVol *ebs.Volume
	var err error
	if cfg.UseInstanceStore {
		// Small, cheap, and the only volume whose loss is unrecoverable.
		valVol, err = ebs.NewVolume(ctx, "validator-state", &ebs.VolumeArgs{
			AvailabilityZone: pulumi.String(cfg.Az),
			Size:             pulumi.Int(cfg.ValidatorVolumeSizeGb),
			Type:             pulumi.String("gp3"),
			// Deliberately NOT provisioned beyond gp3 defaults: #80's 6,000 IOPS
			// / 156 MB/s target the chain-data volume's measured saturation.
			// Applying them here would bill ~$15/mo extra for a 20 GB disk that
			// sees a couple of small writes per epoch.
			Tags: pulumi.StringMap{
				"Name":   pulumi.String("swannynode-mainnet-validator-state"),
				"Backup": pulumi.String(backupTag),
			},
		}, pulumi.Protect(true), pulumi.RetainOnDelete(true))
		if err != nil {
			return nil, err
		}
	}

	var vol *ebs.Volume
	if !cfg.UseInstanceStore {
		vol, err = ebs.NewVolume(ctx, "validator-data", &ebs.VolumeArgs{
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

	return &Storage{Volume: vol, ValidatorVolume: valVol}, nil
}
