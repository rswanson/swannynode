package main

import (
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		// Create a Kubernetes Namespace for Prometheus
		ns, err := corev1.NewNamespace(ctx, "prometheus", &corev1.NamespaceArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name: pulumi.String("prometheus"),
			},
		})
		if err != nil {
			return err
		}

		// Define a label for selectors
		appLabels := pulumi.StringMap{"app": pulumi.String("prometheus")}

		// Create a Deployment for Prometheus
		_, err = appsv1.NewDeployment(ctx, "prometheus-deployment", &appsv1.DeploymentArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Namespace: ns.Metadata.Name(),
			},
			Spec: &appsv1.DeploymentSpecArgs{
				Selector: &metav1.LabelSelectorArgs{
					MatchLabels: appLabels,
				},
				Replicas: pulumi.Int(1),
				Template: &corev1.PodTemplateSpecArgs{
					Metadata: &metav1.ObjectMetaArgs{
						Labels: appLabels,
					},
					Spec: &corev1.PodSpecArgs{
						Containers: corev1.ContainerArray{
							&corev1.ContainerArgs{
								Name:  pulumi.String("prometheus"),
								Image: pulumi.String("prom/prometheus"),
								Ports: corev1.ContainerPortArray{
									&corev1.ContainerPortArgs{
										ContainerPort: pulumi.Int(9090),
									},
								},
							},
						},
					},
				},
			},
		}, pulumi.DependsOn([]pulumi.Resource{ns}))
		if err != nil {
			return err
		}

		// Create a Service to expose Prometheus Deployment
		_, err = corev1.NewService(ctx, "prometheus-service", &corev1.ServiceArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Namespace: ns.Metadata.Name(),
				Name:      pulumi.String("prometheus-service"),
			},
			Spec: &corev1.ServiceSpecArgs{
				Selector: appLabels,
				Ports: corev1.ServicePortArray{
					&corev1.ServicePortArgs{
						Port:       pulumi.Int(9090),
						TargetPort: pulumi.Int(9090),
					},
				},
				Type: pulumi.String("ClusterIP"),
			},
		}, pulumi.DependsOn([]pulumi.Resource{ns}))
		if err != nil {
			return err
		}

		return nil
	})
}
