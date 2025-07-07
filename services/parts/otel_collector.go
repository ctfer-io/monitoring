package parts

import (
	"bytes"
	_ "embed"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"text/template"

	"github.com/pkg/errors"
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"go.uber.org/multierr"
)

type (
	OtelCollector struct {
		pulumi.ResourceState

		cfg        *corev1.ConfigMap
		dep        *appsv1.Deployment
		svcotel    *corev1.Service
		signalsPvc *corev1.PersistentVolumeClaim

		Endpoint           pulumi.StringOutput
		ColdExtractPVCName pulumi.StringPtrOutput
		PodLabels          pulumi.StringMapOutput
	}

	OtelCollectorArgs struct {
		Namespace pulumi.StringInput

		Registry pulumi.StringInput
		registry pulumi.StringOutput

		StorageClassName pulumi.StringInput
		storageClassName pulumi.StringOutput

		StorageSize pulumi.StringInput
		storageSize pulumi.StringOutput

		PVCAccessModes pulumi.StringArrayInput
		pvcAccessModes pulumi.StringArrayOutput

		ColdExtract bool

		JaegerURL     pulumi.StringInput
		PrometheusURL pulumi.StringInput
	}
)

const (
	defaultStorageSize      = "50M"
	defaultStorageClassName = "standard"

	otelVersion = "0.129.1"
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
	otel := &OtelCollector{}

	args = otel.defaults(args)
	if err := otel.check(args); err != nil {
		return nil, err
	}
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

func (*OtelCollector) defaults(args *OtelCollectorArgs) *OtelCollectorArgs {
	if args == nil {
		args = &OtelCollectorArgs{}
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

	// Don't default storage class name -> will select the default one
	// on the K8s cluster.
	args.storageClassName = pulumi.String(defaultStorageClassName).ToStringOutput()
	if args.StorageClassName != nil {
		args.storageClassName = args.StorageClassName.ToStringOutput().ApplyT(func(scm string) string {
			if scm == "" {
				return defaultStorageClassName
			}
			return scm
		}).(pulumi.StringOutput)
	}

	// Default storage size to 50M
	args.storageSize = pulumi.String(defaultStorageSize).ToStringOutput()
	if args.StorageSize != nil {
		args.storageSize = args.StorageSize.ToStringOutput().ApplyT(func(size string) string {
			if size == "" {
				return defaultStorageSize
			}
			return size
		}).(pulumi.StringOutput)
	}

	// Default PVC access modes
	if args.PVCAccessModes == nil {
		args.pvcAccessModes = pulumi.ToStringArray([]string{
			"ReadWriteMany",
		}).ToStringArrayOutput()
	} else {
		args.pvcAccessModes = args.PVCAccessModes.ToStringArrayOutput().ApplyT(func(slc []string) []string {
			if len(slc) == 0 {
				return []string{"ReadWriteMany"}
			}
			return slc
		}).(pulumi.StringArrayOutput)
	}

	return args
}

func (*OtelCollector) check(args *OtelCollectorArgs) (merr error) {
	// First-level checks
	if args.JaegerURL == nil {
		merr = multierr.Append(merr, errors.New("jaeger url is not provided"))
	}
	if args.PrometheusURL == nil {
		merr = multierr.Append(merr, errors.New("prometheus url is not provided"))
	}
	if merr != nil {
		return
	}

	// In-depth checks
	wg := sync.WaitGroup{}
	checks := 2 // number of checks to perform
	wg.Add(checks)
	cerr := make(chan error, checks)

	args.JaegerURL.ToStringOutput().ApplyT(func(u string) error {
		defer wg.Done()

		if err := checkValidURL(u); err != nil {
			cerr <- errors.Wrap(err, "invalid jaeger url")
		}
		return nil
	})
	args.PrometheusURL.ToStringOutput().ApplyT(func(u string) error {
		defer wg.Done()

		if err := checkValidURL(u); err != nil {
			cerr <- errors.Wrap(err, "invalid prometheus url")
		}
		return nil
	})

	wg.Wait()
	close(cerr)

	for err := range cerr {
		merr = multierr.Append(merr, err)
	}
	return merr
}

func (otel *OtelCollector) provision(ctx *pulumi.Context, args *OtelCollectorArgs, opts ...pulumi.ResourceOption) (err error) {
	otel.cfg, err = corev1.NewConfigMap(ctx, "otel-config", &corev1.ConfigMapArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"app.kubernetes.io/component": pulumi.String("otel-collector"),
				"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
		},
		Data: pulumi.StringMap{
			"config": pulumi.All(args.JaegerURL, args.PrometheusURL).ApplyT(func(all []any) (string, error) {
				buf := &bytes.Buffer{}
				if err := otelTemplate.Execute(buf, map[string]any{
					"JaegerURL":     all[0].(string),
					"PrometheusURL": all[1].(string),
					"ColdExtract":   args.ColdExtract,
				}); err != nil {
					return "", err
				}
				return buf.String(), nil
			}).(pulumi.StringOutput),
		},
		Immutable: pulumi.Bool(true),
	}, opts...)
	if err != nil {
		return
	}

	if args.ColdExtract {
		otel.signalsPvc, err = corev1.NewPersistentVolumeClaim(ctx, "signals", &corev1.PersistentVolumeClaimArgs{
			Metadata: metav1.ObjectMetaArgs{
				Namespace: args.Namespace,
				Labels: pulumi.StringMap{
					"app.kubernetes.io/component": pulumi.String("otel-collector"),
					"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
					"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
				},
			},
			Spec: corev1.PersistentVolumeClaimSpecArgs{
				StorageClassName: args.storageClassName,
				AccessModes:      args.pvcAccessModes,
				Resources: corev1.VolumeResourceRequirementsArgs{
					Requests: pulumi.StringMap{
						"storage": args.storageSize,
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
				DefaultMode: pulumi.Int(0644),
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
			Labels: pulumi.StringMap{
				"app.kubernetes.io/name":      pulumi.String("otel-collector"),
				"app.kubernetes.io/version":   pulumi.String(otelVersion),
				"app.kubernetes.io/component": pulumi.String("otel-collector"),
				"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
		},
		Spec: appsv1.DeploymentSpecArgs{
			Replicas: pulumi.Int(1),
			Selector: metav1.LabelSelectorArgs{
				MatchLabels: pulumi.StringMap{
					"app.kubernetes.io/name":      pulumi.String("otel-collector"),
					"app.kubernetes.io/version":   pulumi.String(otelVersion),
					"app.kubernetes.io/component": pulumi.String("otel-collector"),
					"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
					"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
				},
			},
			Template: corev1.PodTemplateSpecArgs{
				Metadata: metav1.ObjectMetaArgs{
					Namespace: args.Namespace,
					Labels: pulumi.StringMap{
						"app.kubernetes.io/name":      pulumi.String("otel-collector"),
						"app.kubernetes.io/version":   pulumi.String(otelVersion),
						"app.kubernetes.io/component": pulumi.String("otel-collector"),
						"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
						"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
					},
				},
				Spec: corev1.PodSpecArgs{
					Containers: corev1.ContainerArray{
						corev1.ContainerArgs{
							Name:  pulumi.String("otel"),
							Image: pulumi.Sprintf("%sotel/opentelemetry-collector-contrib:%s", args.registry, otelVersion),
							Args: pulumi.ToStringArray([]string{
								"--config=/etc/otel-collector/config.yaml",
							}),
							Ports: corev1.ContainerPortArray{
								corev1.ContainerPortArgs{
									Name:          pulumi.String("otlp-grpc"),
									ContainerPort: pulumi.Int(4317),
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
			Labels: pulumi.StringMap{
				"app.kubernetes.io/component": pulumi.String("otel-collector"),
				"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
		},
		Spec: corev1.ServiceSpecArgs{
			Selector: pulumi.StringMap{
				"app.kubernetes.io/name":      pulumi.String("otel-collector"),
				"app.kubernetes.io/version":   pulumi.String(otelVersion),
				"app.kubernetes.io/component": pulumi.String("otel-collector"),
				"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
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

	return
}

func (otel *OtelCollector) outputs(ctx *pulumi.Context, args *OtelCollectorArgs) error {
	otel.Endpoint = pulumi.Sprintf(
		"%s.%s:%d",
		otel.svcotel.Metadata.Name().Elem(),
		otel.svcotel.Metadata.Namespace().Elem(),
		otel.svcotel.Spec.Ports().Index(pulumi.Int(0)).Port(),
	)
	if args.ColdExtract {
		otel.ColdExtractPVCName = otel.signalsPvc.Metadata.Name()
	}
	otel.PodLabels = otel.dep.Spec.Template().Metadata().Labels()

	return ctx.RegisterResourceOutputs(otel, pulumi.Map{
		"endpoint":           otel.Endpoint,
		"coldExtractPVCName": otel.ColdExtractPVCName,
		"podLabels":          otel.PodLabels,
	})
}

func checkValidURL(u string) error {
	_, err := url.Parse(u)
	return err
}
