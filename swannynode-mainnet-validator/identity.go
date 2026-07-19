package main

import (
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/secretsmanager"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const secretPrefix = "mainnet-validator"

type Identity struct {
	Profile *iam.InstanceProfile
	Secrets map[string]*secretsmanager.Secret
}

// createIdentity provisions the three key-material secrets (created EMPTY —
// values are pushed out-of-band so they never enter Pulumi state) and an
// instance role that can read exactly those secrets plus use SSM.
func createIdentity(ctx *pulumi.Context) (*Identity, error) {
	names := []string{"keystore", "keystore-password", "slashing-protection"}
	secrets := map[string]*secretsmanager.Secret{}
	arns := []interface{}{}
	for _, n := range names {
		s, err := secretsmanager.NewSecret(ctx, "validator-secret-"+n, &secretsmanager.SecretArgs{
			Name:        pulumi.String(secretPrefix + "/" + n),
			Description: pulumi.String("mainnet validator " + n + " (value pushed out-of-band)"),
		})
		if err != nil {
			return nil, err
		}
		secrets[n] = s
		arns = append(arns, s.Arn)
	}

	role, err := iam.NewRole(ctx, "validator-instance-role", &iam.RoleArgs{
		AssumeRolePolicy: pulumi.String(`{
			"Version": "2012-10-17",
			"Statement": [{
				"Effect": "Allow",
				"Principal": {"Service": "ec2.amazonaws.com"},
				"Action": "sts:AssumeRole"
			}]
		}`),
	})
	if err != nil {
		return nil, err
	}

	policyJSON := pulumi.All(arns...).ApplyT(func(vs []interface{}) (string, error) {
		return `{
			"Version": "2012-10-17",
			"Statement": [{
				"Effect": "Allow",
				"Action": "secretsmanager:GetSecretValue",
				"Resource": ["` + vs[0].(string) + `", "` + vs[1].(string) + `", "` + vs[2].(string) + `"]
			}]
		}`, nil
	}).(pulumi.StringOutput)

	_, err = iam.NewRolePolicy(ctx, "validator-secrets-read", &iam.RolePolicyArgs{
		Role:   role.ID(),
		Policy: policyJSON,
	})
	if err != nil {
		return nil, err
	}

	_, err = iam.NewRolePolicyAttachment(ctx, "validator-ssm-core", &iam.RolePolicyAttachmentArgs{
		Role:      role.Name,
		PolicyArn: pulumi.String("arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"),
	})
	if err != nil {
		return nil, err
	}

	profile, err := iam.NewInstanceProfile(ctx, "validator-instance-profile", &iam.InstanceProfileArgs{
		Role: role.Name,
	})
	if err != nil {
		return nil, err
	}

	return &Identity{Profile: profile, Secrets: secrets}, nil
}
