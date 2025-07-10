package parts

import (
	"strings"

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

		URL       pulumi.StringOutput
		PodLabels pulumi.StringMapOutput
	}

	PrometheusArgs struct {
		Namespace pulumi.StringInput

		Registry pulumi.StringInput
		registry pulumi.StringOutput
	}
)

const (
	prometheusVersion = "v3.4.2"
)

func NewPrometheus(
	ctx *pulumi.Context,
	name string,
	args *PrometheusArgs,
	opts ...pulumi.ResourceOption,
) (*Prometheus, error) {
	prom := &Prometheus{}

	args = prom.defaults(args)
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

func (*Prometheus) defaults(args *PrometheusArgs) *PrometheusArgs {
	if args == nil {
		args = &PrometheusArgs{}
	}

	// Define private registry if any
	args.registry = pulumi.String("").ToStringOutput()
	if args.Registry != nil {
		args.registry = args.Registry.ToStringPtrOutput().ApplyT(func(in *string) string {
			// No private registry -> defaults to Docker Hub
			if in == nil {
				return ""
			}

			str := *in
			// If one set, make sure it ends with one '/'
			if str != "" && !strings.HasSuffix(str, "/") {
				str = str + "/"
			}
			return str
		}).(pulumi.StringOutput)
	}

	return args
}

func (prom *Prometheus) provision(
	ctx *pulumi.Context,
	args *PrometheusArgs,
	opts ...pulumi.ResourceOption,
) (err error) {
	// ConfigMap
	prom.cfg, err = corev1.NewConfigMap(ctx, "prometheus-conf", &corev1.ConfigMapArgs{
		Immutable: pulumi.BoolPtr(true),
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"app.kubernetes.io/component": pulumi.String("prometheus"),
				"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
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
			Labels: pulumi.StringMap{
				"app.kubernetes.io/name":      pulumi.String("prometheus"),
				"app.kubernetes.io/version":   pulumi.String(prometheusVersion),
				"app.kubernetes.io/component": pulumi.String("prometheus"),
				"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
		},
		Spec: appsv1.DeploymentSpecArgs{
			Selector: metav1.LabelSelectorArgs{
				MatchLabels: pulumi.StringMap{
					"app.kubernetes.io/name":      pulumi.String("prometheus"),
					"app.kubernetes.io/version":   pulumi.String(prometheusVersion),
					"app.kubernetes.io/component": pulumi.String("prometheus"),
					"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
					"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
				},
			},
			Replicas: pulumi.Int(1),
			Template: corev1.PodTemplateSpecArgs{
				Metadata: metav1.ObjectMetaArgs{
					Namespace: args.Namespace,
					Labels: pulumi.StringMap{
						"app.kubernetes.io/name":      pulumi.String("prometheus"),
						"app.kubernetes.io/version":   pulumi.String(prometheusVersion),
						"app.kubernetes.io/component": pulumi.String("prometheus"),
						"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
						"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
					},
				},
				Spec: corev1.PodSpecArgs{
					Containers: corev1.ContainerArray{
						corev1.ContainerArgs{
							Name:  pulumi.String("prometheus"),
							Image: pulumi.Sprintf("%sprom/prometheus:%s", args.registry, prometheusVersion),
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
								DefaultMode: pulumi.Int(0644),
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
			Labels: pulumi.StringMap{
				"app.kubernetes.io/component": pulumi.String("prometheus"),
				"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
		},
		Spec: corev1.ServiceSpecArgs{
			Selector: pulumi.StringMap{
				"app.kubernetes.io/name":      pulumi.String("prometheus"),
				"app.kubernetes.io/version":   pulumi.String(prometheusVersion),
				"app.kubernetes.io/component": pulumi.String("prometheus"),
				"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
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
	prom.URL = pulumi.Sprintf(
		"http://%s:%d",
		prom.svc.Metadata.Name().Elem(),
		prom.svc.Spec.Ports().Index(pulumi.Int(0)).Port(),
	)
	prom.PodLabels = prom.dep.Spec.Template().Metadata().Labels()

	return ctx.RegisterResourceOutputs(prom, pulumi.Map{
		"url":       prom.URL,
		"podLabels": prom.PodLabels,
	})
}
