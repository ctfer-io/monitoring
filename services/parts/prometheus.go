package parts

import (
	"github.com/ctfer-io/monitoring/utils"
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type (
	Prometheus struct {
		pulumi.ResourceState

		cfg *corev1.ConfigMap
		dep *appsv1.Deployment
		svc *corev1.Service

		URL pulumi.StringOutput
	}

	PrometheusArgs struct {
		Namespace pulumi.StringInput
	}
)

func NewPrometheus(ctx *pulumi.Context, name string, args *PrometheusArgs, opts ...pulumi.ResourceOption) (*Prometheus, error) {
	if args == nil {
		args = &PrometheusArgs{}
	}

	prom := &Prometheus{}
	if err := ctx.RegisterComponentResource("ctfer-io:monitoring:prometheus", name, prom, opts...); err != nil {
		return nil, err
	}
	opts = append(opts, pulumi.Parent(prom))
	if err := prom.provision(ctx, args, opts...); err != nil {
		return nil, err
	}
	if err := prom.outputs(ctx); err != nil {
		return nil, err
	}

	return prom, nil
}

func (prom *Prometheus) provision(ctx *pulumi.Context, args *PrometheusArgs, opts ...pulumi.ResourceOption) (err error) {
	labels := pulumi.ToStringMap(map[string]string{
		"category": "monitoring",
		"app":      "prometheus",
	})

	// ConfigMap
	prom.cfg, err = corev1.NewConfigMap(ctx, "prometheus-conf", &corev1.ConfigMapArgs{
		Immutable: pulumi.BoolPtr(true),
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels:    labels,
		},
		Data: pulumi.StringMap{
			"config": pulumi.String(`
scrape_configs:
  - job_name: 'prometheus'
`),
		},
	}, opts...)
	if err != nil {
		return
	}

	// Deployment
	prom.dep, err = appsv1.NewDeployment(ctx, "prometheus", &appsv1.DeploymentArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpecArgs{
			Selector: metav1.LabelSelectorArgs{
				MatchLabels: labels,
			},
			Replicas: pulumi.Int(1),
			Template: corev1.PodTemplateSpecArgs{
				Metadata: metav1.ObjectMetaArgs{
					Namespace: args.Namespace,
					Labels:    labels,
				},
				Spec: corev1.PodSpecArgs{
					Containers: corev1.ContainerArray{
						corev1.ContainerArgs{
							Name:  pulumi.String("prometheus"),
							Image: pulumi.String("prom/prometheus:v2.53.2@sha256:cafe963e591c872d38f3ea41ff8eb22cee97917b7c97b5c0ccd43a419f11f613"),
							Args: pulumi.ToStringArray([]string{
								"--config.file=/etc/prometheus/config.yaml",
								"--web.enable-remote-write-receiver", // Turn on remote write for OtelCollector exporter
							}),
							Ports: corev1.ContainerPortArray{
								corev1.ContainerPortArgs{
									Name:          pulumi.String("metrics"),
									ContainerPort: pulumi.Int(9090),
								},
							},
							VolumeMounts: corev1.VolumeMountArray{
								corev1.VolumeMountArgs{
									Name:      pulumi.String("config-volume"),
									MountPath: pulumi.String("/etc/prometheus"),
									ReadOnly:  pulumi.Bool(true),
								},
							},
						},
					},
					Volumes: corev1.VolumeArray{
						corev1.VolumeArgs{
							Name: pulumi.String("config-volume"),
							ConfigMap: corev1.ConfigMapVolumeSourceArgs{
								Name:        prom.cfg.Metadata.Name(),
								DefaultMode: pulumi.Int(0755),
								Items: corev1.KeyToPathArray{
									corev1.KeyToPathArgs{
										Key:  pulumi.String("config"),
										Path: pulumi.String("config.yaml"),
									},
								},
							},
						},
					},
				},
			},
		},
	}, opts...)
	if err != nil {
		return
	}

	// Service
	prom.svc, err = corev1.NewService(ctx, "prometheus-metrics", &corev1.ServiceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpecArgs{
			Selector:  labels,
			ClusterIP: pulumi.String("None"), // Headless, for DNS purposes
			Ports: corev1.ServicePortArray{
				corev1.ServicePortArgs{
					Name: pulumi.String("metrics"),
					Port: pulumi.Int(9090),
				},
			},
		},
	}, opts...)

	return
}

func (prom *Prometheus) outputs(ctx *pulumi.Context) error {
	prom.URL = utils.Headless(prom.svc).ApplyT(func(hl string) string {
		// TODO support HTTPS e.g. mTLS with Cilium ?
		return "http://" + hl
	}).(pulumi.StringOutput)

	return ctx.RegisterResourceOutputs(prom, pulumi.Map{
		"url": prom.URL,
	})
}
