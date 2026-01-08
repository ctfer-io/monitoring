package parts

import (
	"bytes"
	_ "embed"
	"fmt"
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
	Jaeger struct {
		pulumi.ResourceState

		cfg *corev1.ConfigMap
		dep *appsv1.Deployment
		// Split UI and gRPC API services to enable separating concerns properly.
		// Ths UI svc could be port forwarded if necessary or exposed through an
		// Ingress, but we don't want the gRPC API to be so.
		svcui   *corev1.Service
		svcgrpc *corev1.Service

		// URL to reach out the Jaeger UI
		URL       pulumi.StringOutput
		PodLabels pulumi.StringMapOutput
	}

	JaegerArgs struct {
		Namespace pulumi.StringInput

		Registry pulumi.StringInput
		registry pulumi.StringOutput

		PrometheusURL pulumi.StringInput
	}
)

const (
	jaegerVersion = "2.14.1"
)

//go:embed jaeger-ui.json
var jaegerUI string

//go:embed jaeger-config.yaml.tmpl
var jaegerConfig string
var jaegerTemplate *template.Template

func init() {
	tmpl, err := template.New("jaeger-config").Parse(jaegerConfig)
	if err != nil {
		panic(fmt.Errorf("invalid Jaeger configuration template: %s", err))
	}
	jaegerTemplate = tmpl
}

func NewJaeger(
	ctx *pulumi.Context,
	name string,
	args *JaegerArgs,
	opts ...pulumi.ResourceOption,
) (*Jaeger, error) {
	jgr := &Jaeger{}

	args = jgr.defaults(args)
	if err := jgr.check(args); err != nil {
		return nil, err
	}
	if err := ctx.RegisterComponentResource("ctfer-io:monitoring:jaeger", name, jgr, opts...); err != nil {
		return nil, err
	}
	opts = append(opts, pulumi.Parent(jgr))
	if err := jgr.provision(ctx, args, opts...); err != nil {
		return nil, err
	}
	if err := jgr.outputs(ctx); err != nil {
		return nil, err
	}

	return jgr, nil
}

func (*Jaeger) defaults(args *JaegerArgs) *JaegerArgs {
	if args == nil {
		args = &JaegerArgs{}
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

func (jgr *Jaeger) check(args *JaegerArgs) (merr error) {
	// First-level checks
	if args.PrometheusURL == nil {
		merr = multierr.Append(merr, errors.New("prometheus url is not provided"))
	}
	if merr != nil {
		return
	}

	// In-depth checks
	wg := sync.WaitGroup{}
	checks := 1 // number of checks to perform
	wg.Add(checks)
	cerr := make(chan error, checks)

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

func (jgr *Jaeger) provision(ctx *pulumi.Context, args *JaegerArgs, opts ...pulumi.ResourceOption) (err error) {
	// Create the configuration map for Prometheus-backed monitoring
	jgr.cfg, err = corev1.NewConfigMap(ctx, "spm-config", &corev1.ConfigMapArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels: pulumi.StringMap{
				"app.kubernetes.io/name":      pulumi.String("jaeger"),
				"app.kubernetes.io/version":   pulumi.String(jaegerVersion),
				"app.kubernetes.io/component": pulumi.String("jaeger"),
				"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
			Namespace: args.Namespace,
		},
		Data: pulumi.StringMap{
			"jaeger-ui.json": pulumi.String(jaegerUI),
			"config.yaml": args.PrometheusURL.ToStringOutput().ApplyT(func(prometheusUrl string) (string, error) {
				buf := &bytes.Buffer{}
				if err := jaegerTemplate.Execute(buf, map[string]any{
					"PrometheusURL": prometheusUrl,
				}); err != nil {
					return "", err
				}
				return buf.String(), nil
			}).(pulumi.StringOutput),
		},
	}, opts...)
	if err != nil {
		return
	}

	// Deployment
	jgr.dep, err = appsv1.NewDeployment(ctx, "jaeger", &appsv1.DeploymentArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"app.kubernetes.io/name":      pulumi.String("jaeger"),
				"app.kubernetes.io/version":   pulumi.String(jaegerVersion),
				"app.kubernetes.io/component": pulumi.String("jaeger"),
				"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
		},
		Spec: appsv1.DeploymentSpecArgs{
			Selector: metav1.LabelSelectorArgs{
				MatchLabels: pulumi.StringMap{
					"app.kubernetes.io/name":      pulumi.String("jaeger"),
					"app.kubernetes.io/version":   pulumi.String(jaegerVersion),
					"app.kubernetes.io/component": pulumi.String("jaeger"),
					"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
					"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
				},
			},
			Replicas: pulumi.Int(1),
			Template: corev1.PodTemplateSpecArgs{
				Metadata: metav1.ObjectMetaArgs{
					Namespace: args.Namespace,
					Labels: pulumi.StringMap{
						"app.kubernetes.io/name":      pulumi.String("jaeger"),
						"app.kubernetes.io/version":   pulumi.String(jaegerVersion),
						"app.kubernetes.io/component": pulumi.String("jaeger"),
						"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
						"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
					},
				},
				Spec: corev1.PodSpecArgs{
					Containers: corev1.ContainerArray{
						corev1.ContainerArgs{
							Name:  pulumi.String("jaeger"),
							Image: pulumi.Sprintf("%sjaegertracing/jaeger:%s", args.registry, jaegerVersion),
							Args: pulumi.ToStringArray([]string{
								"--config=/etc/jaeger/config.yaml",
							}),
							Ports: corev1.ContainerPortArray{
								corev1.ContainerPortArgs{
									Name:          pulumi.String("ui"),
									ContainerPort: pulumi.Int(16686),
								},
								corev1.ContainerPortArgs{
									Name:          pulumi.String("grpc"),
									ContainerPort: pulumi.Int(4317),
								},
							},
							VolumeMounts: corev1.VolumeMountArray{
								corev1.VolumeMountArgs{
									Name:      pulumi.String("config-volume"),
									MountPath: pulumi.String("/etc/jaeger"),
									ReadOnly:  pulumi.Bool(true),
								},
							},
						},
					},
					Volumes: corev1.VolumeArray{
						corev1.VolumeArgs{
							Name: pulumi.String("config-volume"),
							ConfigMap: corev1.ConfigMapVolumeSourceArgs{
								Name:        jgr.cfg.Metadata.Name(),
								DefaultMode: pulumi.Int(0644),
								Items: corev1.KeyToPathArray{
									corev1.KeyToPathArgs{
										Key:  pulumi.String("jaeger-ui.json"),
										Path: pulumi.String("jaeger-ui.json"),
									},
									corev1.KeyToPathArgs{
										Key:  pulumi.String("config.yaml"),
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

	// Services
	// => One dedicated to the UI, will be port-forwarded if necessary
	jgr.svcui, err = corev1.NewService(ctx, "jaeger-ui", &corev1.ServiceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"app.kubernetes.io/component": pulumi.String("jaeger"),
				"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
		},
		Spec: corev1.ServiceSpecArgs{
			Selector: pulumi.StringMap{
				"app.kubernetes.io/component": pulumi.String("jaeger"),
				"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
			ClusterIP: pulumi.String("None"), // Headless, for DNS purposes
			Ports: corev1.ServicePortArray{
				corev1.ServicePortArgs{
					Name: pulumi.String("ui"),
					Port: pulumi.Int(16686),
				},
			},
		},
	}, opts...)
	if err != nil {
		return
	}

	// => The grpc endpoint to send data to
	jgr.svcgrpc, err = corev1.NewService(ctx, "jaeger-grpc", &corev1.ServiceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"app.kubernetes.io/component": pulumi.String("jaeger"),
				"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
		},
		Spec: corev1.ServiceSpecArgs{
			Selector: pulumi.StringMap{
				"app.kubernetes.io/component": pulumi.String("jaeger"),
				"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
			ClusterIP: pulumi.String("None"), // Headless, for DNS purposes
			Ports: corev1.ServicePortArray{
				corev1.ServicePortArgs{
					Name: pulumi.String("grpc"),
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

func (jgr *Jaeger) outputs(ctx *pulumi.Context) error {
	jgr.URL = pulumi.Sprintf(
		"http://%s:%d",
		jgr.svcgrpc.Metadata.Name().Elem(),
		jgr.svcgrpc.Spec.Ports().Index(pulumi.Int(0)).Port(),
	)
	jgr.PodLabels = jgr.dep.Spec.Template().Metadata().Labels()

	return ctx.RegisterResourceOutputs(jgr, pulumi.Map{
		"url":       jgr.URL,
		"podLabels": jgr.PodLabels,
	})
}
