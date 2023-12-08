package main

import (
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	storagev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/storage/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		// fetch the rethTargetIp from the stack config
		cfg := config.New(ctx, "")
		rethTargetIp := cfg.Require("rethTargetIp") // Assuming vpcId is a string

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
		appLabelPrometheus := pulumi.StringMap{"app": pulumi.String("prometheus")}
		appLabelGrafana := pulumi.StringMap{"app": pulumi.String("grafana")}

		// Create ConfigMap for Prometheus
		prometheusConfig, err := corev1.NewConfigMap(ctx, "prometheus-config", &corev1.ConfigMapArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Namespace: ns.Metadata.Name(),
				Name:      pulumi.String("prometheus-config"),
			},
			Data: pulumi.StringMap{
				"prometheus.yml": pulumi.String(`
global:
  scrape_interval: 15s
scrape_configs:
  - job_name: prometheus
    static_configs:
      - targets: ['localhost:9090']
  - job_name: reth
    static_configs:
      - targets: ['` + rethTargetIp + `']`),
			},
		}, pulumi.DependsOn([]pulumi.Resource{ns}))
		if err != nil {
			return err
		}

		// Create a Deployment for Prometheus
		_, err = appsv1.NewDeployment(ctx, "prometheus-deployment", &appsv1.DeploymentArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Namespace: ns.Metadata.Name(),
			},
			Spec: &appsv1.DeploymentSpecArgs{
				Selector: &metav1.LabelSelectorArgs{
					MatchLabels: appLabelPrometheus,
				},
				Replicas: pulumi.Int(1),
				Template: &corev1.PodTemplateSpecArgs{
					Metadata: &metav1.ObjectMetaArgs{
						Labels: appLabelPrometheus,
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
								VolumeMounts: corev1.VolumeMountArray{
									&corev1.VolumeMountArgs{
										Name:      pulumi.String("prometheus-config"),
										MountPath: pulumi.String("/etc/prometheus/"),
									},
								},
							},
						},
						Volumes: corev1.VolumeArray{
							&corev1.VolumeArgs{
								Name: pulumi.String("prometheus-config"),
								ConfigMap: &corev1.ConfigMapVolumeSourceArgs{
									Name: prometheusConfig.Metadata.Name(),
								},
							},
						},
					},
				},
			},
		}, pulumi.DependsOn([]pulumi.Resource{ns, prometheusConfig}))
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
				Selector: appLabelPrometheus,
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
		// Create the ebs gp2 storage class
		_, err = storagev1.NewStorageClass(ctx, "ebs-sc", &storagev1.StorageClassArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name: pulumi.String("aws-gp2"), // Name of the StorageClass
			},
			Provisioner:          pulumi.String("ebs.csi.aws.com"), // Amazon EBS CSI driver
			VolumeBindingMode:    pulumi.String("WaitForFirstConsumer"),
			AllowVolumeExpansion: pulumi.Bool(true),       // Allow volume expansion
			ReclaimPolicy:        pulumi.String("Delete"), // Automatically delete EBS volume when PVC is deleted
			Parameters: pulumi.StringMap{
				"type": pulumi.String("gp2"), // The type of EBS volume
			},
		})

		if err != nil {
			return err
		}

		// create a persistent volume claim with dynamic provisioning for grafana to use for storage
		_, err = corev1.NewPersistentVolumeClaim(ctx, "grafana-pvc", &corev1.PersistentVolumeClaimArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name:      pulumi.String("grafana-pvc"), // Name of the PVC
				Namespace: pulumi.String("prometheus"),  // Namespace to create the PVC in
			},
			Spec: &corev1.PersistentVolumeClaimSpecArgs{
				AccessModes: pulumi.StringArray{pulumi.String("ReadWriteOnce")}, // ReadWriteOnce is the only supported mode for EBS
				Resources: corev1.ResourceRequirementsArgs{
					Requests: pulumi.StringMap{
						"storage": pulumi.String("5Gi"), // Request 5 GiB of space
					},
				},
				StorageClassName: pulumi.String("aws-gp2"), // Use the 'aws-gp2' storage class
			},
		})
		if err != nil {
			return err
		}

		// Create a grafana Deployment
		grafana, err := appsv1.NewDeployment(ctx, "grafana-deployment", &appsv1.DeploymentArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Namespace: ns.Metadata.Name(),
			},
			Spec: &appsv1.DeploymentSpecArgs{
				Selector: &metav1.LabelSelectorArgs{
					MatchLabels: appLabelGrafana,
				},
				Replicas: pulumi.Int(1),
				Template: &corev1.PodTemplateSpecArgs{
					Metadata: &metav1.ObjectMetaArgs{
						Labels: appLabelGrafana,
					},
					Spec: &corev1.PodSpecArgs{
						Containers: corev1.ContainerArray{
							&corev1.ContainerArgs{
								Name:            pulumi.String("grafana"),
								Image:           pulumi.String("grafana/grafana-oss:latest"),
								ImagePullPolicy: pulumi.String("Always"),
								Ports: corev1.ContainerPortArray{
									&corev1.ContainerPortArgs{
										ContainerPort: pulumi.Int(3000),
									},
								},
								VolumeMounts: corev1.VolumeMountArray{
									&corev1.VolumeMountArgs{
										Name:      pulumi.String("grafana-pv"),
										MountPath: pulumi.String("/var/lib/grafana"),
									},
								},
							},
						},
						Volumes: corev1.VolumeArray{
							&corev1.VolumeArgs{
								Name: pulumi.String("grafana-pv"),
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSourceArgs{
									ClaimName: pulumi.String("grafana-pvc"),
								},
							},
						},
						SecurityContext: &corev1.PodSecurityContextArgs{
							FsGroup: pulumi.Int(472),
							SupplementalGroups: pulumi.IntArray{
								pulumi.Int(472),
								pulumi.Int(0),
							},
						},
					},
				},
			},
		}, pulumi.DependsOn([]pulumi.Resource{ns}))
		if err != nil {
			return err
		}

		// Create a Service to expose grafana Deployment
		_, err = corev1.NewService(ctx, "grafana-service", &corev1.ServiceArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Namespace: ns.Metadata.Name(),
				Name:      pulumi.String("grafana-service"),
			},
			Spec: &corev1.ServiceSpecArgs{
				Selector: appLabelGrafana,
				Ports: corev1.ServicePortArray{
					&corev1.ServicePortArgs{
						Port:       pulumi.Int(3000),
						TargetPort: pulumi.Int(3000),
					},
				},
				Type: pulumi.String("ClusterIP"),
			},
		}, pulumi.DependsOn([]pulumi.Resource{ns, grafana}))
		if err != nil {
			return err
		}

		// Create a ServiceMonitor for Prometheus
		_, err = corev1.NewService(ctx, "prometheus-service-monitor", &corev1.ServiceArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Namespace: ns.Metadata.Name(),
				Name:      pulumi.String("prometheus-service-monitor"),
				Labels:    appLabelPrometheus,
			},
			Spec: &corev1.ServiceSpecArgs{
				Selector: appLabelPrometheus,
				Ports: corev1.ServicePortArray{
					&corev1.ServicePortArgs{
						Port: pulumi.Int(9090),
					},
				},
				Type: pulumi.String("ClusterIP"),
			},
		}, pulumi.DependsOn([]pulumi.Resource{ns}))
		if err != nil {
			return err
		}

		// Create a ServiceMonitor for grafana
		_, err = corev1.NewService(ctx, "grafana-service-monitor", &corev1.ServiceArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Namespace: ns.Metadata.Name(),
				Name:      pulumi.String("grafana-service-monitor"),
				Labels:    appLabelGrafana,
			},
			Spec: &corev1.ServiceSpecArgs{
				Selector: appLabelGrafana,
				Ports: corev1.ServicePortArray{
					&corev1.ServicePortArgs{
						Port: pulumi.Int(3000),
					},
				},
				Type: pulumi.String("ClusterIP"),
			},
		}, pulumi.DependsOn([]pulumi.Resource{ns, grafana}))
		if err != nil {
			return err
		}

		return nil
	})
}
