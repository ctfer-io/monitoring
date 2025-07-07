package parts

import (
	"strings"

	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type (
	Jaeger struct {
		pulumi.ResourceState

		dep *appsv1.Deployment
		// Split UI and gRPC API services to enable separating concerns properly.
		// Ths UI svc could be port forwarded if necessary or exposed through an
		// Ingress, but we don't want the gRPC API to be so.
		svcui   *corev1.Service
		svcgrpc *corev1.Service

		// URL to reach out the Jaeger UI
		URL pulumi.StringOutput
	}

	JaegerArgs struct {
		// Global attributes
		Namespace pulumi.StringInput

		Registry pulumi.StringInput
		registry pulumi.StringOutput

		// Prometheus-related attributes.
		// If no Prometheus URL is defined, the metrics dashboard
		PrometheusURL pulumi.StringPtrInput
	}
)

const (
	jaegerAIOVersion = "2.8.0"
)

func NewJaeger(ctx *pulumi.Context, name string, args *JaegerArgs, opts ...pulumi.ResourceOption) (*Jaeger, error) {
	jgr := &Jaeger{}

	args = jgr.defaults(args)
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

func (jgr *Jaeger) provision(ctx *pulumi.Context, args *JaegerArgs, opts ...pulumi.ResourceOption) (err error) {
	// Deployment
	depEnv := corev1.EnvVarArray{}
	if args.PrometheusURL != nil {
		depEnv = append(depEnv,
			corev1.EnvVarArgs{
				Name:  pulumi.String("METRICS_STORAGE_TYPE"),
				Value: pulumi.String("prometheus"),
			},
			corev1.EnvVarArgs{
				Name:  pulumi.String("PROMETHEUS_SERVER_URL"),
				Value: args.PrometheusURL,
			},
			// Following required for normalizing, see https://www.jaegertracing.io/docs/next-release/spm/#viewing-logs
			corev1.EnvVarArgs{
				Name:  pulumi.String("PROMETHEUS_QUERY_NORMALIZE_CALLS"),
				Value: pulumi.String("true"),
			},
			corev1.EnvVarArgs{
				Name:  pulumi.String("PROMETHEUS_QUERY_NORMALIZE_DURATION"),
				Value: pulumi.String("true"),
			},
		)
	}

	jgr.dep, err = appsv1.NewDeployment(ctx, "jaeger-all-in-one", &appsv1.DeploymentArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"app.kubernetes.io/name":      pulumi.String("jaeger"),
				"app.kubernetes.io/version":   pulumi.String(otelVersion),
				"app.kubernetes.io/component": pulumi.String("jaeger"),
				"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
		},
		Spec: appsv1.DeploymentSpecArgs{
			Selector: metav1.LabelSelectorArgs{
				MatchLabels: pulumi.StringMap{
					"app.kubernetes.io/name":      pulumi.String("jaeger"),
					"app.kubernetes.io/version":   pulumi.String(otelVersion),
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
						"app.kubernetes.io/version":   pulumi.String(otelVersion),
						"app.kubernetes.io/component": pulumi.String("jaeger"),
						"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
						"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
					},
				},
				Spec: corev1.PodSpecArgs{
					Containers: corev1.ContainerArray{
						corev1.ContainerArgs{
							Name:  pulumi.String("jaeger"),
							Image: pulumi.Sprintf("%sjaegertracing/jaeger:%s", args.registry, jaegerAIOVersion),
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
							Env: depEnv,
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

	return ctx.RegisterResourceOutputs(jgr, pulumi.Map{
		"url":       jgr.URL,
		"podLabels": jgr.dep.Spec.Template().Metadata().Labels(),
	})
}
