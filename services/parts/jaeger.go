package parts

import (
	"strings"

	"github.com/ctfer-io/monitoring/utils"
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

		Registry pulumi.StringPtrInput
		registry pulumi.StringOutput

		// TODO add Traefik configuration

		// Prometheus-related attributes
		PrometheusURL pulumi.StringPtrInput
	}
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
	jgr.outputs()

	return jgr, nil
}

func (cm *Jaeger) defaults(args *JaegerArgs) *JaegerArgs {
	if args == nil {
		args = &JaegerArgs{}
	}

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
	hasPrometheus := args.PrometheusURL != nil

	labels := pulumi.ToStringMap(map[string]string{
		"category": "monitoring",
		"app":      "jaeger",
	})

	// Deployment
	depEnv := corev1.EnvVarArray{}
	if hasPrometheus {
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
							Name:  pulumi.String("jaeger"),
							Image: pulumi.Sprintf("%sjaegertracing/all-in-one:1.60.0", args.registry),
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
	jgr.svcui, err = corev1.NewService(ctx, "jaeger-ui", &corev1.ServiceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpecArgs{
			Selector:  labels,
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
	jgr.svcgrpc, err = corev1.NewService(ctx, "jaeger-grpc", &corev1.ServiceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpecArgs{
			Selector:  labels,
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

func (jgr *Jaeger) outputs() {
	jgr.URL = utils.Headless(jgr.svcgrpc).ApplyT(func(hl string) string {
		// TODO support HTTPS e.g. mTLS with Cilium ?
		return "http://" + hl
	}).(pulumi.StringOutput)
}
