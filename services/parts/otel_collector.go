package parts

import (
	"bytes"
	_ "embed"
	"fmt"
	"text/template"

	"github.com/ctfer-io/monitoring/utils"
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type (
	OtelCollector struct {
		pulumi.ResourceState

		cfg        *corev1.ConfigMap
		dep        *appsv1.Deployment
		svcotel    *corev1.Service
		svcprom    *corev1.Service
		signalsPvc *corev1.PersistentVolumeClaim

		Endpoint           pulumi.StringOutput
		ColdExtractPVCName pulumi.StringPtrOutput
	}

	OtelCollectorArgs struct {
		Namespace pulumi.StringInput

		ColdExtract bool

		JaegerURL     pulumi.StringInput
		PrometheusURL pulumi.StringInput
	}
)

//go:embed otel-config.yaml.tmpl
var otelConfig string
var otelTemplate *template.Template

func init() {
	tmpl, err := template.New("otel-config").Parse(otelConfig)
	if err != nil {
		panic(fmt.Errorf("invalid OTEL configuration template: %s", err))
	}
	otelTemplate = tmpl
}

func NewOtelCollector(ctx *pulumi.Context, name string, args *OtelCollectorArgs, opts ...pulumi.ResourceOption) (*OtelCollector, error) {
	if args == nil {
		args = &OtelCollectorArgs{}
	}

	otel := &OtelCollector{}
	if err := ctx.RegisterComponentResource("ctfer-io:monitoring:otel-collector", name, otel, opts...); err != nil {
		return nil, err
	}
	opts = append(opts, pulumi.Parent(otel))
	if err := otel.provision(ctx, args, opts...); err != nil {
		return nil, err
	}
	if err := otel.outputs(ctx, args); err != nil {
		return nil, err
	}

	return otel, nil
}

func (otel *OtelCollector) provision(ctx *pulumi.Context, args *OtelCollectorArgs, opts ...pulumi.ResourceOption) (err error) {
	labels := pulumi.ToStringMap(map[string]string{
		"category": "monitoring",
		"app":      "otel-collector",
	})

	otel.cfg, err = corev1.NewConfigMap(ctx, "otel-config", &corev1.ConfigMapArgs{
		Immutable: pulumi.Bool(true),
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels:    labels,
		},
		Data: pulumi.StringMap{
			"config": pulumi.All(args.JaegerURL, args.PrometheusURL).ApplyT(func(all []any) string {
				buf := &bytes.Buffer{}
				if err := otelTemplate.Execute(buf, map[string]any{
					"JaegerURL":     all[0].(string),
					"PrometheusURL": all[1].(string),
					"ColdExtract":   args.ColdExtract,
				}); err != nil {
					panic(err)
				}
				str := buf.String()
				return str
			}).(pulumi.StringOutput),
		},
	}, opts...)
	if err != nil {
		return
	}

	if args.ColdExtract {
		otel.signalsPvc, err = corev1.NewPersistentVolumeClaim(ctx, "signals", &corev1.PersistentVolumeClaimArgs{
			Metadata: metav1.ObjectMetaArgs{
				Namespace: args.Namespace,
				Labels: pulumi.StringMap{
					"app.kubernetes.io/component": pulumi.String("otel"),
					"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				},
			},
			Spec: corev1.PersistentVolumeClaimSpecArgs{
				StorageClassName: pulumi.String("longhorn"),
				AccessModes: pulumi.ToStringArray([]string{
					"ReadWriteMany",
				}),
				Resources: corev1.VolumeResourceRequirementsArgs{
					Requests: pulumi.StringMap{
						"storage": pulumi.String("5Gi"),
					},
				},
			},
		}, opts...)
		if err != nil {
			return
		}
	}

	vmounts := corev1.VolumeMountArray{
		corev1.VolumeMountArgs{
			Name:      pulumi.String("config-volume"),
			MountPath: pulumi.String("/etc/otel-collector"),
			ReadOnly:  pulumi.Bool(true),
		},
	}
	vs := corev1.VolumeArray{
		corev1.VolumeArgs{
			Name: pulumi.String("config-volume"),
			ConfigMap: corev1.ConfigMapVolumeSourceArgs{
				Name:        otel.cfg.Metadata.Name(),
				DefaultMode: pulumi.Int(0755),
				Items: corev1.KeyToPathArray{
					corev1.KeyToPathArgs{
						Key:  pulumi.String("config"),
						Path: pulumi.String("config.yaml"),
					},
				},
			},
		},
	}
	if args.ColdExtract {
		vmounts = append(vmounts,
			corev1.VolumeMountArgs{
				Name:      pulumi.String("signals"),
				MountPath: pulumi.String("/data/collector"),
			},
		)
		vs = append(vs,
			corev1.VolumeArgs{
				Name: pulumi.String("signals"),
				PersistentVolumeClaim: corev1.PersistentVolumeClaimVolumeSourceArgs{
					ClaimName: otel.signalsPvc.Metadata.Name().Elem(),
				},
			},
		)
	}

	otel.dep, err = appsv1.NewDeployment(ctx, "otel", &appsv1.DeploymentArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpecArgs{
			Replicas: pulumi.Int(1),
			Selector: metav1.LabelSelectorArgs{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpecArgs{
				Metadata: metav1.ObjectMetaArgs{
					Namespace: args.Namespace,
					Labels:    labels,
				},
				Spec: corev1.PodSpecArgs{
					Containers: corev1.ContainerArray{
						corev1.ContainerArgs{
							Name:  pulumi.String("otel"),
							Image: pulumi.String("otel/opentelemetry-collector-contrib:0.107.0@sha256:b65527791431d76d058b2813748a3f4a8912540d7b23beac2f6b4e02c872f5b7"),
							Args: pulumi.ToStringArray([]string{
								"--config=/etc/otel-collector/config.yaml",
							}),
							Ports: corev1.ContainerPortArray{
								corev1.ContainerPortArgs{
									Name:          pulumi.String("otlp-grpc"),
									ContainerPort: pulumi.Int(4317),
								},
								corev1.ContainerPortArgs{
									Name:          pulumi.String("metrics"),
									ContainerPort: pulumi.Int(9090),
								},
							},
							VolumeMounts: vmounts,
						},
					},
					Volumes: vs,
				},
			},
		},
	}, opts...)
	if err != nil {
		return
	}

	otel.svcotel, err = corev1.NewService(ctx, "otlp-grpc", &corev1.ServiceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpecArgs{
			Selector:  labels,
			ClusterIP: pulumi.String("None"), // Headless, for DNS purposes
			Ports: corev1.ServicePortArray{
				corev1.ServicePortArgs{
					Name: pulumi.String("otlp-grpc"),
					Port: pulumi.Int(4317),
				},
			},
		},
	}, opts...)
	if err != nil {
		return
	}

	otel.svcprom, err = corev1.NewService(ctx, "otlp-metrics", &corev1.ServiceArgs{
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
	if err != nil {
		return
	}

	return
}

func (otel *OtelCollector) outputs(ctx *pulumi.Context, args *OtelCollectorArgs) error {
	otel.Endpoint = utils.Headless(otel.svcotel)
	if args.ColdExtract {
		otel.ColdExtractPVCName = otel.signalsPvc.Metadata.Name()
	}

	return ctx.RegisterResourceOutputs(otel, pulumi.Map{
		"endpoint":           otel.Endpoint,
		"coldExtractPVCName": otel.ColdExtractPVCName,
	})
}
