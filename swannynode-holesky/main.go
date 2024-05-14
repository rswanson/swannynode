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
		// Define static string variables
		rethDataVolumeName := pulumi.String("reth-config-data")

		rethTomlData, err := os.ReadFile("config/reth.toml")
		if err != nil {
			return err
		}

		// Create a ConfigMap with the content of reth.toml
		configMap, err := corev1.NewConfigMap(ctx, "reth-config", &corev1.ConfigMapArgs{
			Data: pulumi.StringMap{
				"reth.toml": pulumi.String(string(rethTomlData)),
			},
		})
		if err != nil {
			return err
		}

		// Create the gp3 storage class
		_, err = storagev1.NewStorageClass(ctx, "gp3", &storagev1.StorageClassArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name: pulumi.String("aws-gp3"),
			},
			Provisioner:          pulumi.String("ebs.csi.aws.com"),
			VolumeBindingMode:    pulumi.String("WaitForFirstConsumer"),
			AllowVolumeExpansion: pulumi.Bool(true),       // Allow volume expansion
			ReclaimPolicy:        pulumi.String("Delete"), // Automatically delete EBS volume when PVC is deleted
			Parameters: pulumi.StringMap{
				"type": pulumi.String("gp3"), // The type of EBS volume
				"iops": pulumi.String("16000"),
			},
		})
		if err != nil {
			return err
		}

		// Define the PersistentVolumeClaim for 1.5TB storage
		storageSize := pulumi.String("100Gi") // 30Gi size for holesky
		_, err = corev1.NewPersistentVolumeClaim(ctx, "reth-data", &corev1.PersistentVolumeClaimArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name: rethDataVolumeName,
			},
			Spec: &corev1.PersistentVolumeClaimSpecArgs{
				AccessModes: pulumi.StringArray{pulumi.String("ReadWriteOnce")}, // This should match your requirements
				Resources: &corev1.VolumeResourceRequirementsArgs{
					Requests: pulumi.StringMap{
						"storage": storageSize,
					},
				},
				StorageClassName: pulumi.String("aws-gp3"),
			},
		})
		if err != nil {
			return err
		}

		// Define the PersistentVolumeClaim for 30Gi storage for lighthouse
		storageSize = pulumi.String("150Gi") // 30Gi size for holesky
		_, err = corev1.NewPersistentVolumeClaim(ctx, "lighthouse-data", &corev1.PersistentVolumeClaimArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name: pulumi.String("lighthouse-data"),
			},
			Spec: &corev1.PersistentVolumeClaimSpecArgs{
				AccessModes: pulumi.StringArray{pulumi.String("ReadWriteOnce")}, // This should match your requirements
				Resources: &corev1.VolumeResourceRequirementsArgs{
					Requests: pulumi.StringMap{
						"storage": storageSize,
					},
				},
				StorageClassName: pulumi.String("aws-gp3"),
			},
		})
		if err != nil {
			return err
		}

		// Get jwt from pulumi secret
		cfg := config.New(ctx, "")
		jwt := cfg.RequireSecret("execution-jwt")
		// Create a secret for the execution jwt
		secret, err := corev1.NewSecret(ctx, "execution-jwt", &corev1.SecretArgs{
			StringData: pulumi.StringMap{
				"jwt.hex": jwt,
			},
		})
		if err != nil {
			return err
		}

		// Create a ConfigMap with the content of lighthouse.toml
		lighthouseTomlData, err := os.ReadFile("config/lighthouse.toml")
		if err != nil {
			return err
		}
		lighthouseConfigData, err := corev1.NewConfigMap(ctx, "lighthouse-config", &corev1.ConfigMapArgs{
			Data: pulumi.StringMap{
				"lighthouse.toml": pulumi.String(string(lighthouseTomlData)),
			},
		})
		if err != nil {
			return err
		}

		// Define the StatefulSet for the 'reth' container with a configmap volume and a data persistent volume
		_, err = appsv1.NewStatefulSet(ctx, "reth-set", &appsv1.StatefulSetArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name: pulumi.String("reth"),
			},
			Spec: &appsv1.StatefulSetSpecArgs{
				Replicas: pulumi.Int(1),
				Selector: &metav1.LabelSelectorArgs{
					MatchLabels: pulumi.StringMap{
						"app": pulumi.String("reth"),
					},
				},
				Template: &corev1.PodTemplateSpecArgs{
					Metadata: &metav1.ObjectMetaArgs{
						Labels: pulumi.StringMap{
							"app": pulumi.String("reth"),
						},
					},
					Spec: &corev1.PodSpecArgs{
						Containers: corev1.ContainerArray{
							corev1.ContainerArgs{
								Name:  pulumi.String("reth"),
								Image: pulumi.String("ghcr.io/paradigmxyz/reth:latest"),
								Command: pulumi.StringArray{
									pulumi.String("reth"),
									pulumi.String("node"),
									pulumi.String("--chain"),
									pulumi.String("holesky"),
									pulumi.String("--authrpc.jwtsecret"),
									pulumi.String("/etc/reth/execution-jwt/jwt.hex"),
									pulumi.String("--authrpc.addr"),
									pulumi.String("0.0.0.0"),
									pulumi.String("--authrpc.port"),
									pulumi.String("8551"),
									pulumi.String("--datadir"),
									pulumi.String("/root/.local/share/reth/holesky"),
									pulumi.String("--metrics"),
									pulumi.String("0.0.0.0:9001"),
									pulumi.String("--http"),
									pulumi.String("--http.addr"),
									pulumi.String("0.0.0.0"),
									pulumi.String("--http.api"),
									pulumi.String("eth,net,trace,txpool,web3,rpc,debug"),
									// pulumi.String("--config"),
									// pulumi.String("/etc/reth/reth.toml"),
								},
								Ports: corev1.ContainerPortArray{
									corev1.ContainerPortArgs{
										ContainerPort: pulumi.Int(30303),
									},
									corev1.ContainerPortArgs{
										ContainerPort: pulumi.Int(30303),
										Protocol:      pulumi.String("UDP"),
									},
									corev1.ContainerPortArgs{
										ContainerPort: pulumi.Int(9001),
									},
									corev1.ContainerPortArgs{
										ContainerPort: pulumi.Int(8545),
									},
									corev1.ContainerPortArgs{
										ContainerPort: pulumi.Int(8551),
									},
								},
								VolumeMounts: corev1.VolumeMountArray{
									corev1.VolumeMountArgs{
										Name:      pulumi.String("reth-config"),
										MountPath: pulumi.String("/etc/reth"),
									},
									corev1.VolumeMountArgs{
										Name:      rethDataVolumeName,
										MountPath: pulumi.String("/root/.local/share/reth"),
									},
									corev1.VolumeMountArgs{
										Name:      pulumi.String("execution-jwt"),
										MountPath: pulumi.String("/etc/reth/execution-jwt"),
									},
								},
							},
						},
						Volumes: corev1.VolumeArray{
							corev1.VolumeArgs{
								Name: pulumi.String("reth-config"),
								ConfigMap: &corev1.ConfigMapVolumeSourceArgs{
									Name: configMap.Metadata.Name(),
								},
							},
							corev1.VolumeArgs{
								Name: rethDataVolumeName,
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSourceArgs{
									ClaimName: rethDataVolumeName,
								},
							},
							corev1.VolumeArgs{
								Name: pulumi.String("execution-jwt"),
								Secret: &corev1.SecretVolumeSourceArgs{
									SecretName: secret.Metadata.Name(),
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			return err
		}

		// Create a Service for external ports
		_, err = corev1.NewService(ctx, "reth-p2pnet-service", &corev1.ServiceArgs{
			Spec: &corev1.ServiceSpecArgs{
				Selector: pulumi.StringMap{"app": pulumi.String("reth")},
				Type:     pulumi.String("NodePort"),
				Ports: corev1.ServicePortArray{
					&corev1.ServicePortArgs{
						Port: pulumi.Int(30303),
						Name: pulumi.String("p2p-tcp"),
					},
					&corev1.ServicePortArgs{
						Port:     pulumi.Int(30303),
						Protocol: pulumi.String("UDP"),
						Name:     pulumi.String("p2p-udp"),
					},
				},
			},
		})
		if err != nil {
			return err
		}

		// Create a service for internal ports
		_, err = corev1.NewService(ctx, "reth-internal-service", &corev1.ServiceArgs{
			Spec: &corev1.ServiceSpecArgs{
				Selector: pulumi.StringMap{"app": pulumi.String("reth")},
				Type:     pulumi.String("ClusterIP"),
				Ports: corev1.ServicePortArray{
					corev1.ServicePortArgs{
						Port: pulumi.Int(9001),
						Name: pulumi.String("metrics"),
					},
					corev1.ServicePortArgs{
						Port: pulumi.Int(8551),
						Name: pulumi.String("p2p"),
					},
				},
			},
			Metadata: &metav1.ObjectMetaArgs{
				Name: pulumi.String("reth-internal-service"),
			},
		})
		if err != nil {
			return err
		}

		// Create a stateful set to run a lighthouse node with a configmap volume and a data persistent volume
		_, err = appsv1.NewStatefulSet(ctx, "lighthouse-set", &appsv1.StatefulSetArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name: pulumi.String("lighthouse"),
			},
			Spec: &appsv1.StatefulSetSpecArgs{
				Replicas: pulumi.Int(1),
				Selector: &metav1.LabelSelectorArgs{
					MatchLabels: pulumi.StringMap{
						"app": pulumi.String("lighthouse"),
					},
				},
				Template: &corev1.PodTemplateSpecArgs{
					Metadata: &metav1.ObjectMetaArgs{
						Labels: pulumi.StringMap{
							"app": pulumi.String("lighthouse"),
						},
					},
					Spec: &corev1.PodSpecArgs{
						Containers: corev1.ContainerArray{
							corev1.ContainerArgs{
								Name:  pulumi.String("lighthouse"),
								Image: pulumi.String("sigp/lighthouse:latest"),
								Command: pulumi.StringArray{
									pulumi.String("lighthouse"),
									pulumi.String("bn"),
									pulumi.String("--datadir"),
									pulumi.String("/root/.local/share/lighthouse/holesky"),
									pulumi.String("--network"),
									pulumi.String("holesky"),
									pulumi.String("--checkpoint-sync-url"),
									pulumi.String("https://holesky.checkpoint.sigp.io/"),
									pulumi.String("--execution-jwt"),
									pulumi.String("/secrets/jwt.hex"),
									pulumi.String("--http"),
									pulumi.String("--execution-endpoint"),
									pulumi.String("http://reth-internal-service.default:8551"),
									pulumi.String("--disable-deposit-contract-sync"),
									pulumi.String("--metrics"),
								},
								Ports: corev1.ContainerPortArray{
									corev1.ContainerPortArgs{
										ContainerPort: pulumi.Int(9000),
									},
									corev1.ContainerPortArgs{
										ContainerPort: pulumi.Int(9000),
										Protocol:      pulumi.String("UDP"),
									},
									corev1.ContainerPortArgs{
										ContainerPort: pulumi.Int(9001),
										Protocol:      pulumi.String("UDP"),
									},
									corev1.ContainerPortArgs{
										ContainerPort: pulumi.Int(5054),
									},
									corev1.ContainerPortArgs{
										ContainerPort: pulumi.Int(5052),
									},
								},
								VolumeMounts: corev1.VolumeMountArray{
									corev1.VolumeMountArgs{
										Name:      pulumi.String("lighthouse-config"),
										MountPath: pulumi.String("/etc/lighthouse"),
									},
									corev1.VolumeMountArgs{
										Name:      pulumi.String("lighthouse-data"),
										MountPath: pulumi.String("/root/.local/share/lighthouse/holesky"),
									},
									corev1.VolumeMountArgs{
										Name:      pulumi.String("execution-jwt"),
										MountPath: pulumi.String("/secrets"),
									},
								},
							},
						},
						DnsPolicy: pulumi.String("ClusterFirst"),
						Volumes: corev1.VolumeArray{
							corev1.VolumeArgs{
								Name: pulumi.String("lighthouse-config"),
								ConfigMap: &corev1.ConfigMapVolumeSourceArgs{
									Name: lighthouseConfigData.Metadata.Name(),
								},
							},
							corev1.VolumeArgs{
								Name: pulumi.String("lighthouse-data"),
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSourceArgs{
									ClaimName: pulumi.String("lighthouse-data"),
								},
							},
							corev1.VolumeArgs{
								Name: pulumi.String("execution-jwt"),
								Secret: &corev1.SecretVolumeSourceArgs{
									SecretName: secret.Metadata.Name(),
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			return err
		}

		// Create ingress for lighthouse p2p traffic on port 9000
		_, err = corev1.NewService(ctx, "lighthouse-p2p-service", &corev1.ServiceArgs{
			Spec: &corev1.ServiceSpecArgs{
				Selector: pulumi.StringMap{"app": pulumi.String("lighthouse")},
				Type:     pulumi.String("NodePort"),
				Ports: corev1.ServicePortArray{
					corev1.ServicePortArgs{
						Port: pulumi.Int(9000),
						Name: pulumi.String("p2p-tcp"),
					},
					corev1.ServicePortArgs{
						Port:     pulumi.Int(9000),
						Protocol: pulumi.String("UDP"),
						Name:     pulumi.String("p2p-udp"),
					},
				},
			},
		})
		if err != nil {
			return err
		}

		// Create ingress for the reth rpc traffic on port 8545
		rethRpcService, err := corev1.NewService(ctx, "reth-rpc-service", &corev1.ServiceArgs{
			Spec: &corev1.ServiceSpecArgs{
				Selector: pulumi.StringMap{"app": pulumi.String("reth")},
				Type:     pulumi.String("NodePort"),
				Ports: corev1.ServicePortArray{
					corev1.ServicePortArgs{
						Port:       pulumi.Int(8545),
						TargetPort: pulumi.Int(8545),
					},
				},
			},
			Metadata: &metav1.ObjectMetaArgs{
				Name: pulumi.String("reth-rpc-service"),
			},
		})
		if err != nil {
			return err
		}

		// Create an ingress for grafanaService
		rethIngress, err := networkingv1.NewIngress(ctx, "grafana-ingress", &networkingv1.IngressArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name: pulumi.String("reth-holesky-ingress"),
				Annotations: pulumi.StringMap{
					"kubernetes.io/ingress.class":                    pulumi.String("alb"),
					"alb.ingress.kubernetes.io/scheme":               pulumi.String("internet-facing"),
					"alb.ingress.kubernetes.io/target-type":          pulumi.String("instance"),
					"alb.ingress.kubernetes.io/certificate-arn":      pulumi.String(cfg.Require("rethIngressTlsCertArn")),
					"alb.ingress.kubernetes.io/listen-ports":         pulumi.String(`[{"HTTP": 80}, {"HTTPS":443}]`),
					"alb.ingress.kubernetes.io/actions.ssl-redirect": pulumi.String(`{"Type": "redirect", "RedirectConfig": { "Protocol": "HTTPS", "Port": "443", "StatusCode": "HTTP_301"}}`),
				},
			},
			Spec: &networkingv1.IngressSpecArgs{
				Rules: &networkingv1.IngressRuleArray{
					&networkingv1.IngressRuleArgs{
						Host: pulumi.String(cfg.Require("publicHostname")),
						Http: &networkingv1.HTTPIngressRuleValueArgs{
							Paths: &networkingv1.HTTPIngressPathArray{
								&networkingv1.HTTPIngressPathArgs{
									Path:     pulumi.String("/"), // Assuming you want to route all traffic to Grafana
									PathType: pulumi.String("Prefix"),
									Backend: &networkingv1.IngressBackendArgs{
										Service: &networkingv1.IngressServiceBackendArgs{
											Name: pulumi.String("reth-rpc-service"),
											Port: &networkingv1.ServiceBackendPortArgs{
												Number: pulumi.Int(8545),
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}, pulumi.DependsOn([]pulumi.Resource{rethRpcService}))
		if err != nil {
			return err
		}

		lbHostname := rethIngress.Status.ApplyT(func(status *networkingv1.IngressStatus) (string, error) {
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
		_, err = route53.NewRecord(ctx, "reth-dns", &route53.RecordArgs{
			ZoneId: pulumi.String(zoneId),
			Name:   pulumi.String(cfg.Require("publicHostname")),
			Records: pulumi.StringArray{
				lbHostname,
			},
			Ttl:  pulumi.Int(300),
			Type: pulumi.String("CNAME"),
		})
		if err != nil {
			return err
		}
		return nil
	})

}
