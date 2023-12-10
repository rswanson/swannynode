package main

import (
	"os"

	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/eks"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/iam"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	helm "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		// Load the configuration.
		cfg := config.New(ctx, "")
		vpcId := cfg.Require("vpcId") // Assuming vpcId is a string
		oidc := pulumi.String(cfg.Require("oidcUrl"))
		oidcWithProtocol := pulumi.String("https://" + oidc)
		sub := pulumi.String(oidc + ":sub")
		aud := pulumi.String(oidc + ":aud")
		arnAccountSection := pulumi.String(cfg.Require("arnAccountSection"))
		oidcThumbprint := pulumi.String(cfg.Require("oidcThumbprint"))

		// Get the list of subnet IDs in the VPC.
		subnets, err := ec2.GetSubnets(ctx, &ec2.GetSubnetsArgs{
			Filters: []ec2.GetSubnetsFilter{
				{
					Name:   "vpc-id",
					Values: []string{vpcId},
				},
			},
		})
		if err != nil {
			return err
		}

		subnetIds := subnets.Ids

		// Create OIDC provider for the cluster.
		_, err = iam.NewOpenIdConnectProvider(ctx, "oidcProvider", &iam.OpenIdConnectProviderArgs{
			Url: oidcWithProtocol,
			ClientIdLists: pulumi.StringArray{
				pulumi.String("sts.amazonaws.com"),
				pulumi.String("system:serviceaccount:kube-system:ebs-csi-controller-sa"),
			},
			ThumbprintLists: pulumi.StringArray{
				oidcThumbprint,
			},
		})
		if err != nil {
			return err
		}

		// Create an IAM role for the EKS cluster.
		eksAssumeRolePolicy, err := os.ReadFile("config/eks/assume_role_policy.json")
		if err != nil {
			return err
		}

		eksRole, err := iam.NewRole(ctx, "eksRole", &iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(eksAssumeRolePolicy),
		})
		if err != nil {
			return err
		}

		// Attach the AmazonEKSClusterPolicy managed policy to the role.
		_, err = iam.NewRolePolicyAttachment(ctx, "eksRolePolicy", &iam.RolePolicyAttachmentArgs{
			Role:      eksRole.Name,
			PolicyArn: pulumi.String("arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"),
		})
		if err != nil {
			return err
		}

		// Create an EKS cluster in the specified VPC. Skip creation of the detault nodegroup
		cluster, err := eks.NewCluster(ctx, "eksCluster", &eks.ClusterArgs{
			Name: pulumi.String("swannynode-cluster"),
			VpcConfig: &eks.ClusterVpcConfigArgs{
				SubnetIds: pulumi.ToStringArray(subnetIds),
			},
			RoleArn: eksRole.Arn,
		})
		if err != nil {
			return err
		}

		// OIDC provider association for cluster auth with role bindings
		oidcProvider, err := eks.NewIdentityProviderConfig(ctx, "oidcProviderConfig", &eks.IdentityProviderConfigArgs{
			ClusterName: cluster.Name,
			Oidc: &eks.IdentityProviderConfigOidcArgs{
				ClientId:                   pulumi.String("sts.amazonaws.com"),
				IdentityProviderConfigName: pulumi.String("oidcProviderConfig"),
				IssuerUrl:                  oidcWithProtocol,
			},
		})
		if err != nil {
			return err
		}

		// Create the IAM role for the nodegroup.
		ec2AssumeRolePolicy, err := os.ReadFile("config/ec2/assume_role_policy.json")
		if err != nil {
			return err
		}

		nodegroupRole, err := iam.NewRole(ctx, "nodeGroupRole", &iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(ec2AssumeRolePolicy),
		})
		if err != nil {
			return err
		}

		// Attach the AmazonEKSWorkerNodePolicy managed policy to the role.
		_, err = iam.NewRolePolicyAttachment(ctx, "nodeGroupRolePolicy", &iam.RolePolicyAttachmentArgs{
			Role:      nodegroupRole.Name,
			PolicyArn: pulumi.String("arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy"),
		})
		if err != nil {
			return err
		}

		// Attach the AmazonEKS_CNI_Policy managed policy to the role.
		_, err = iam.NewRolePolicyAttachment(ctx, "nodeGroupRolePolicy2", &iam.RolePolicyAttachmentArgs{
			Role:      nodegroupRole.Name,
			PolicyArn: pulumi.String("arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy"),
		})
		if err != nil {
			return err
		}

		// Attach the AmazonEC2ContainerRegistryReadOnly managed policy to the role.
		_, err = iam.NewRolePolicyAttachment(ctx, "nodeGroupRolePolicy3", &iam.RolePolicyAttachmentArgs{
			Role:      nodegroupRole.Name,
			PolicyArn: pulumi.String("arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly"),
		})
		if err != nil {
			return err
		}

		kubeProxy, err := eks.NewAddon(ctx, "kube-proxy", &eks.AddonArgs{
			AddonName:                pulumi.String("kube-proxy"),
			ClusterName:              cluster.Name,
			ResolveConflictsOnUpdate: pulumi.String("PRESERVE"),
		})
		if err != nil {
			return err
		}

		vpcCni, err := eks.NewAddon(ctx, "vpc-cni", &eks.AddonArgs{
			AddonName:                pulumi.String("vpc-cni"),
			ClusterName:              cluster.Name,
			ResolveConflictsOnUpdate: pulumi.String("PRESERVE"),
			ResolveConflictsOnCreate: pulumi.String("OVERWRITE"),
		})
		if err != nil {
			return err
		}

		serviceAccountRole, err := iam.NewRole(ctx, "ebs-csi-driver-sa-role", &iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(`{
				"Version": "2012-10-17",
				"Statement": [
				  {
					"Effect": "Allow",
					"Principal": {
					  "Federated": "` + arnAccountSection + `:oidc-provider/` + oidc + `"
					},
					"Action": "sts:AssumeRoleWithWebIdentity",
					"Condition": {
					  "StringEquals": {
						"` + aud + `": "sts.amazonaws.com",
						"` + sub + `": "system:serviceaccount:kube-system:ebs-csi-controller-sa"
					  }
					}
				  }
				]
			  }
			  `),
		})
		if err != nil {
			return err
		}

		// Attach the AmazonEBSCSIDriverPolicy managed policy to the role.
		_, err = iam.NewRolePolicyAttachment(ctx, "ebs-csi-driver-sa-ra", &iam.RolePolicyAttachmentArgs{
			Role:      serviceAccountRole.Name,
			PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AmazonEBSCSIDriverPolicy"),
		})
		if err != nil {
			return err
		}

		// Create a ServiceAccount for the EBS CSI driver.
		_, err = corev1.NewServiceAccount(ctx, "ebs-csi-driver-sa", &corev1.ServiceAccountArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name:      pulumi.String("ebs-csi-controller-sa"),
				Namespace: pulumi.String("kube-system"),
			},
		})
		if err != nil {
			return err
		}

		// Create a Managed Node Group.
		_, err = eks.NewNodeGroup(ctx, "nodeGroup", &eks.NodeGroupArgs{
			ClusterName:   cluster.Name,
			NodeGroupName: pulumi.String("swannynode-nodegroup"),
			InstanceTypes: pulumi.StringArray{pulumi.String("m7g.xlarge")},
			ScalingConfig: &eks.NodeGroupScalingConfigArgs{
				DesiredSize: pulumi.Int(2),
				MinSize:     pulumi.Int(2),
				MaxSize:     pulumi.Int(2),
			},
			DiskSize:    pulumi.Int(20),
			SubnetIds:   pulumi.ToStringArray(subnetIds),
			NodeRoleArn: nodegroupRole.Arn,
			AmiType:     pulumi.String("AL2_ARM_64"),
		}, pulumi.DependsOn([]pulumi.Resource{kubeProxy, vpcCni}))
		if err != nil {
			return err
		}

		_, err = eks.NewAddon(ctx, "coredns", &eks.AddonArgs{
			AddonName:                pulumi.String("coredns"),
			ClusterName:              cluster.Name,
			ResolveConflictsOnUpdate: pulumi.String("PRESERVE"),
			ResolveConflictsOnCreate: pulumi.String("NONE"),
		})
		if err != nil {
			return err
		}

		_, err = eks.NewAddon(ctx, "ebs-csi-driver", &eks.AddonArgs{
			AddonName:                pulumi.String("aws-ebs-csi-driver"),
			ClusterName:              cluster.Name,
			ServiceAccountRoleArn:    serviceAccountRole.Arn,
			ResolveConflictsOnUpdate: pulumi.String("PRESERVE"),
			ResolveConflictsOnCreate: pulumi.String("NONE"),
		}, pulumi.DependsOn([]pulumi.Resource{oidcProvider}))
		if err != nil {
			return err
		}

		// Create IAM policy from json file config/aws-lb-controller/iam-policy.json
		iamPolicyFile, err := os.ReadFile("config/aws-lb-controller/iam_policy.json")
		if err != nil {
			return err
		}

		iamPolicy, err := iam.NewPolicy(ctx, "aws-lb-controller-policy", &iam.PolicyArgs{
			Description: pulumi.String("Policy for AWS Load Balancer Controller"),
			Policy:      pulumi.String(iamPolicyFile),
		})
		if err != nil {
			return err
		}

		// Create IAM role for AWS Load Balancer Controller
		awsLbControllerRole, err := iam.NewRole(ctx, "aws-lb-controller-role", &iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(`{
				"Version": "2012-10-17",
				"Statement": [
				  {
					"Effect": "Allow",
					"Principal": {
					  "Federated": "` + arnAccountSection + `:oidc-provider/` + oidc + `"
					},
					"Action": "sts:AssumeRoleWithWebIdentity",
					"Condition": {
					  "StringEquals": {
						"` + aud + `": "sts.amazonaws.com",
						"` + sub + `": "system:serviceaccount:kube-system:aws-load-balancer-controller"
					  }
					}
				  }
				]
			  }
			  `),
		})
		if err != nil {
			return err
		}

		// Attach the AWSLoadBalancerControllerIAMPolicy managed policy to the role.
		_, err = iam.NewRolePolicyAttachment(ctx, "aws-lb-controller-role-policy", &iam.RolePolicyAttachmentArgs{
			Role:      awsLbControllerRole.Name,
			PolicyArn: iamPolicy.Arn,
		})
		if err != nil {
			return err
		}

		// Create a ServiceAccount for the AWS Load Balancer Controller.
		_, err = corev1.NewServiceAccount(ctx, "aws-lb-controller-sa", &corev1.ServiceAccountArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name:      pulumi.String("aws-load-balancer-controller"),
				Namespace: pulumi.String("kube-system"),
			},
		})
		if err != nil {
			return err
		}

		// Install the AWS Load Balancer Controller Helm chart.
		_, err = helm.NewChart(ctx, "aws-lb-controller", helm.ChartArgs{
			Chart:     pulumi.String("aws-load-balancer-controller"),
			Namespace: pulumi.String("kube-system"),
			FetchArgs: &helm.FetchArgs{
				Repo: pulumi.String("https://aws.github.io/eks-charts"),
			},
			Values: pulumi.Map{
				"clusterName": cluster.Name,
				"region":      pulumi.String(cfg.Require("region")),
				"vpcId":       pulumi.String(vpcId),
				"serviceAccount": pulumi.Map{
					"create": pulumi.Bool(false),
					"name":   pulumi.String("aws-load-balancer-controller"),
				},
			},
		}, pulumi.DependsOn([]pulumi.Resource{awsLbControllerRole}))
		if err != nil {
			return err
		}

		return nil
	})
}
