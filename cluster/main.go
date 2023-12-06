package main

import (
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/eks"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		// Load the configuration.
		cfg := config.New(ctx, "")
		vpcId := cfg.Require("vpcId") // Assuming vpcId is a string

		// Get the list of subnet IDs in the VPC.
		subnets, err := ec2.GetSubnets(ctx, &ec2.GetSubnetsArgs{
			Filters: []ec2.GetSubnetsFilter{
				ec2.GetSubnetsFilter{
					Name:   "vpc-id",
					Values: []string{vpcId},
				},
			},
		})
		if err != nil {
			return err
		}

		subnetIds := subnets.Ids

		// Create an IAM role for the EKS cluster.
		eksRole, err := iam.NewRole(ctx, "eksRole", &iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(`{
				"Version": "2008-10-17",
				"Statement": [{
					"Sid": "",
					"Effect": "Allow",
					"Principal": {
						"Service": "eks.amazonaws.com"
					},
					"Action": "sts:AssumeRole"
				}]
			}`),
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

		// Create the IAM role for the nodegroup.
		nodegroupRole, err := iam.NewRole(ctx, "nodeGroupRole", &iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(`{
				"Version": "2008-10-17",
				"Statement": [{
					"Sid": "",
					"Effect": "Allow",
					"Principal": {
						"Service": "ec2.amazonaws.com"
					},
					"Action": "sts:AssumeRole"
				}]
			}`),
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

		serviceAccountRole, err := iam.NewRole(ctx, "ebs-csi-driver-sa", &iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(`{
						"Version": "2008-10-17",
						"Statement": [{
							"Sid": "",
							"Effect": "Allow",
							"Principal": {
								"Service": "eks.amazonaws.com"
							},
							"Action": "sts:AssumeRole"
						}]
					}`),
		})
		if err != nil {
			return err
		}

		_, err = iam.NewRolePolicyAttachment(ctx, "ebs-csi-driver-sa", &iam.RolePolicyAttachmentArgs{
			Role:      nodegroupRole.Name,
			PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AmazonEBSCSIDriverPolicy"),
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
		})
		if err != nil {
			return err
		}

		return nil
	})
}

// generateKubeconfig generates a kubeconfig file given the endpoint, certificate authority, and cluster name.
func generateKubeconfig(endpoint string, caData string, clusterName string) string {
	kubeconfigTemplate := `
apiVersion: v1
clusters:
- cluster:
    server: %v
    certificate-authority-data: %v
  name: %v
contexts:
- context:
    cluster: %v
    user: %v
  name: %v
current-context: %v
kind: Config
preferences: {}
users:
- name: %v
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1alpha1
      command: aws
      args:
        - "eks"
        - "get-token"
        - "--cluster-name"
        - "%v"
      # Uncomment the following line to use an AWS profile with this kubeconfig
      # env:
      #   - name: AWS_PROFILE
      #     value: "your-aws-profile"
`
	return fmt.Sprintf(kubeconfigTemplate, endpoint, caData, clusterName, clusterName, clusterName, clusterName, clusterName, clusterName, clusterName)
}
