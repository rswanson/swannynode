package main

import (
	"fmt"
	"os"

	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/route53"
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	networkingv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/networking/v1"
	storagev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/storage/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		// fetch the rethTargetIp from the stack config
		cfg := config.New(ctx, "")
		rethTargetIp := cfg.Require("rethTargetIp")
		validatorTargetIp := cfg.Require("validatorTargetIp")
		consensusTargetIp := cfg.Require("consensusTargetIp")
		recordName := cfg.Require("recordName")

		// dashboard vars
		rethDashboard, err := os.ReadFile("config/grafana/dashboards/reth-overview.json")
		if err != nil {
			return err // Handle the error according to your needs.
		}
		rethDashboardConfig, err := os.ReadFile("config/grafana/dashboard-config.yaml")
		if err != nil {
			return err // Handle the error according to your needs.
		}

		// Create a Kubernetes Namespace for Prometheus
		ns, err := corev1.NewNamespace(ctx, "prometheus", &corev1.NamespaceArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name: pulumi.String("monitoring"),
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
      - targets: ['` + rethTargetIp + `']
  - job_name: validator
    static_configs:
      - targets: ['` + validatorTargetIp + `']
  - job_name: beacon_node
    static_configs:
      - targets: ['` + consensusTargetIp + `']
  - job_name: holesky_reth
    static_configs:
	  - targets: ['reth-internal-service.default:9001']
`),
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
				Namespace: ns.Metadata.Name(),           // Namespace to create the PVC in
			},
			Spec: &corev1.PersistentVolumeClaimSpecArgs{
				AccessModes: pulumi.StringArray{pulumi.String("ReadWriteOnce")}, // ReadWriteOnce is the only supported mode for EBS
				Resources: corev1.VolumeResourceRequirementsArgs{
					Requests: pulumi.StringMap{
						"storage": pulumi.String("5Gi"),
					},
				},
				StorageClassName: pulumi.String("aws-gp2"), // Use the 'aws-gp2' storage class
			},
		})
		if err != nil {
			return err
		}
		//read grafana.ini config file into var
		grafanaConfigFile, err := os.ReadFile("config/grafana/grafana.ini")
		if err != nil {
			return err // Handle the error according to your needs.
		}

		// create configmap for grafana
		grafanaConfig, err := corev1.NewConfigMap(ctx, "grafana-config", &corev1.ConfigMapArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Namespace: ns.Metadata.Name(),
				Name:      pulumi.String("grafana-config"),
			},
			Data: pulumi.StringMap{
				"grafana.ini": pulumi.String(grafanaConfigFile),
			},
		}, pulumi.DependsOn([]pulumi.Resource{ns}))
		if err != nil {
			return err
		}

		// read prometheus.yaml from file
		grafanaPrometheusDatasourceConfigFile, err := os.ReadFile("config/grafana/prometheus.yaml")
		if err != nil {
			return err // Handle the error according to your needs.
		}

		grafanaPrometheusDatasourceConfig, err := corev1.NewConfigMap(ctx, "grafana-prometheus-datasource-config", &corev1.ConfigMapArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Namespace: ns.Metadata.Name(),
				Name:      pulumi.String("grafana-prometheus-datasource-config"),
			},
			Data: pulumi.StringMap{
				"prometheus.yaml": pulumi.String(grafanaPrometheusDatasourceConfigFile),
			},
		}, pulumi.DependsOn([]pulumi.Resource{ns}))
		if err != nil {
			return err
		}

		grafanaRethDashboardConfig, err := corev1.NewConfigMap(ctx, "grafana-reth-dashboard-config", &corev1.ConfigMapArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Namespace: ns.Metadata.Name(),
				Name:      pulumi.String("grafana-reth-dashboard-config"),
			},
			Data: pulumi.StringMap{
				"dashboard.yaml": pulumi.String(rethDashboardConfig),
			},
		}, pulumi.DependsOn([]pulumi.Resource{ns}))
		if err != nil {
			return err
		}

		grafanaDashboardConfigMap, err := corev1.NewConfigMap(ctx, "grafana-dashboard-config", &corev1.ConfigMapArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Namespace: ns.Metadata.Name(),
				Name:      pulumi.String("grafana-dashboard-config"),
			},
			Data: pulumi.StringMap{
				"reth-overview.json": pulumi.String(rethDashboard),
			},
		}, pulumi.DependsOn([]pulumi.Resource{ns}))
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
									&corev1.VolumeMountArgs{
										Name:      pulumi.String("grafana-config"),
										MountPath: pulumi.String("/etc/grafana"),
									},
									&corev1.VolumeMountArgs{
										Name:      pulumi.String("grafana-prometheus-datasource-config"),
										MountPath: pulumi.String("/etc/grafana/provisioning/datasources"),
									},
									&corev1.VolumeMountArgs{
										Name:      pulumi.String("grafana-reth-dashboard-json"),
										MountPath: pulumi.String("/etc/grafana/provisioning/dashboards"),
									},
									&corev1.VolumeMountArgs{
										Name:      pulumi.String("grafana-dashboard-config"),
										MountPath: pulumi.String("/etc/dashboards"),
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
							&corev1.VolumeArgs{
								Name: pulumi.String("grafana-config"),
								ConfigMap: &corev1.ConfigMapVolumeSourceArgs{
									Name: grafanaConfig.Metadata.Name(),
								},
							},
							&corev1.VolumeArgs{
								Name: pulumi.String("grafana-prometheus-datasource-config"),
								ConfigMap: &corev1.ConfigMapVolumeSourceArgs{
									Name: grafanaPrometheusDatasourceConfig.Metadata.Name(),
								},
							},
							&corev1.VolumeArgs{
								Name: pulumi.String("grafana-reth-dashboard-json"),
								ConfigMap: &corev1.ConfigMapVolumeSourceArgs{
									Name: grafanaRethDashboardConfig.Metadata.Name(),
								},
							},
							&corev1.VolumeArgs{
								Name: pulumi.String("grafana-dashboard-config"),
								ConfigMap: &corev1.ConfigMapVolumeSourceArgs{
									Name: grafanaDashboardConfigMap.Metadata.Name(),
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
		grafanaService, err := corev1.NewService(ctx, "grafana-service", &corev1.ServiceArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Namespace: ns.Metadata.Name(),
				Name:      pulumi.String("grafana-service"),
			},
			Spec: &corev1.ServiceSpecArgs{
				Selector: appLabelGrafana,
				Type:     pulumi.String("NodePort"),
				Ports: corev1.ServicePortArray{
					&corev1.ServicePortArgs{
						Port:       pulumi.Int(80),
						TargetPort: pulumi.Int(3000),
					},
				},
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

		// Create an ingress for grafanaService
		grafanaIngress, err := networkingv1.NewIngress(ctx, "grafana-ingress", &networkingv1.IngressArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Namespace: ns.Metadata.Name(),
				Name:      pulumi.String("grafana-ingress"),
				Annotations: pulumi.StringMap{
					"kubernetes.io/ingress.class":                    pulumi.String("alb"),
					"alb.ingress.kubernetes.io/scheme":               pulumi.String("internet-facing"),
					"alb.ingress.kubernetes.io/target-type":          pulumi.String("instance"),
					"alb.ingress.kubernetes.io/certificate-arn":      pulumi.String(cfg.Require("grafanaTlsCertId")),
					"alb.ingress.kubernetes.io/listen-ports":         pulumi.String(`[{"HTTP": 80}, {"HTTPS":443}]`),
					"alb.ingress.kubernetes.io/actions.ssl-redirect": pulumi.String(`{"Type": "redirect", "RedirectConfig": { "Protocol": "HTTPS", "Port": "443", "StatusCode": "HTTP_301"}}`),
				},
			},
			Spec: &networkingv1.IngressSpecArgs{
				Rules: &networkingv1.IngressRuleArray{
					&networkingv1.IngressRuleArgs{
						Http: &networkingv1.HTTPIngressRuleValueArgs{
							Paths: &networkingv1.HTTPIngressPathArray{
								&networkingv1.HTTPIngressPathArgs{
									Path:     pulumi.String("/"), // Assuming you want to route all traffic to Grafana
									PathType: pulumi.String("Prefix"),
									Backend: &networkingv1.IngressBackendArgs{
										Service: &networkingv1.IngressServiceBackendArgs{
											Name: pulumi.String("grafana-service"),
											Port: &networkingv1.ServiceBackendPortArgs{
												Number: pulumi.Int(80),
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}, pulumi.DependsOn([]pulumi.Resource{ns, grafanaService}))
		if err != nil {
			return err
		}

		lbHostname := grafanaIngress.Status.ApplyT(func(status *networkingv1.IngressStatus) (string, error) {
			// status.LoadBalancer could be nil or have an empty Ingress slice.
			if status.LoadBalancer == nil || len(status.LoadBalancer.Ingress) == 0 {
				return "", fmt.Errorf("no ingress load balancer information found")
			}
			// Return the hostname of the load balancer, make sure to handle possible nil values.
			if status.LoadBalancer.Ingress[0].Hostname != nil {
				return *status.LoadBalancer.Ingress[0].Hostname, nil
			}
			return "", fmt.Errorf("load balancer ingress hostname is nil")
		}).(pulumi.StringInput)

		zoneId := cfg.Require("zoneId")

		// create route53 record for grafana
		_, err = route53.NewRecord(ctx, "grafana-route53", &route53.RecordArgs{
			Name: pulumi.String(recordName),
			Records: pulumi.StringArray{
				lbHostname,
			},
			Ttl:    pulumi.Int(300),
			Type:   pulumi.String("CNAME"),
			ZoneId: pulumi.String(zoneId),
		})
		if err != nil {
			return err
		}

		return nil
	})
}
