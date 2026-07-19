package main

import (
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type Network struct {
	SecurityGroup *ec2.SecurityGroup
	SubnetId      string
	Eip           *ec2.Eip
}

func anywhere() pulumi.StringArray {
	return pulumi.StringArray{pulumi.String("0.0.0.0/0")}
}

func ingress(proto string, port int) *ec2.SecurityGroupIngressArgs {
	return &ec2.SecurityGroupIngressArgs{
		Protocol:   pulumi.String(proto),
		FromPort:   pulumi.Int(port),
		ToPort:     pulumi.Int(port),
		CidrBlocks: anywhere(),
	}
}

// createNetwork looks up the default VPC subnet in the configured AZ and
// creates the validator security group and Elastic IP. Metrics ports are
// deliberately NOT exposed; only p2p and SSH.
func createNetwork(ctx *pulumi.Context, cfg StackConfig) (*Network, error) {
	vpc, err := ec2.LookupVpc(ctx, &ec2.LookupVpcArgs{Default: pulumi.BoolRef(true)})
	if err != nil {
		return nil, err
	}
	subnet, err := ec2.LookupSubnet(ctx, &ec2.LookupSubnetArgs{
		VpcId:            pulumi.StringRef(vpc.Id),
		AvailabilityZone: pulumi.StringRef(cfg.Az),
		DefaultForAz:     pulumi.BoolRef(true),
	})
	if err != nil {
		return nil, err
	}

	sg, err := ec2.NewSecurityGroup(ctx, "validator-sg", &ec2.SecurityGroupArgs{
		VpcId:       pulumi.String(vpc.Id),
		Description: pulumi.String("mainnet validator: p2p + ssh only"),
		Ingress: ec2.SecurityGroupIngressArray{
			ingress("tcp", 22),
			ingress("tcp", 30303), // reth p2p
			ingress("udp", 30303),
			ingress("tcp", 9000), // lighthouse p2p
			ingress("udp", 9000),
			ingress("udp", 9001), // lighthouse quic
		},
		Egress: ec2.SecurityGroupEgressArray{
			&ec2.SecurityGroupEgressArgs{
				Protocol:   pulumi.String("-1"),
				FromPort:   pulumi.Int(0),
				ToPort:     pulumi.Int(0),
				CidrBlocks: anywhere(),
			},
		},
		Tags: pulumi.StringMap{"Name": pulumi.String("swannynode-mainnet-validator")},
	})
	if err != nil {
		return nil, err
	}

	eip, err := ec2.NewEip(ctx, "validator-eip", &ec2.EipArgs{
		Domain: pulumi.String("vpc"),
		Tags:   pulumi.StringMap{"Name": pulumi.String("swannynode-mainnet-validator")},
	})
	if err != nil {
		return nil, err
	}

	return &Network{SecurityGroup: sg, SubnetId: subnet.Id, Eip: eip}, nil
}
