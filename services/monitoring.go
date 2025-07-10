package services

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	netwv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/networking/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ctfer-io/monitoring/services/parts"
)

type (
	Monitoring struct {
		pulumi.ResourceState

		ns     *parts.Namespace
		otel   *parts.OtelCollector
		jaeger *parts.Jaeger
		prom   *parts.Prometheus

		inotelntp *netwv1.NetworkPolicy
		otelntp   *netwv1.NetworkPolicy
		jgrntp    *netwv1.NetworkPolicy
		promntp   *netwv1.NetworkPolicy

		Namespace pulumi.StringOutput
		OTEL      MonitoringOTELOutput
	}

	MonitoringOTELOutput struct {
		Endpoint           pulumi.StringOutput
		ColdExtractPVCName pulumi.StringPtrOutput
		PodLabels          pulumi.StringMapOutput
	}

	MonitoringArgs struct {
		Registry         pulumi.StringInput
		StorageClassName pulumi.StringInput
		StorageSize      pulumi.StringInput
		PVCAccessModes   pulumi.StringArrayInput

		ColdExtract bool
	}
)

func NewMonitoring(
	ctx *pulumi.Context,
	name string,
	args *MonitoringArgs,
	opts ...pulumi.ResourceOption,
) (*Monitoring, error) {
	mon := &Monitoring{}

	args = mon.defaults(args)
	if err := ctx.RegisterComponentResource("ctfer-io:monitoring:monitoring", name, mon, opts...); err != nil {
		return nil, err
	}
	opts = append(opts, pulumi.Parent(mon))
	if err := mon.provision(ctx, args, opts...); err != nil {
		return nil, err
	}
	if err := mon.outputs(ctx); err != nil {
		return nil, err
	}

	return mon, nil
}

func (*Monitoring) defaults(args *MonitoringArgs) *MonitoringArgs {
	if args == nil {
		args = &MonitoringArgs{}
	}

	return args
}

func (mon *Monitoring) provision(
	ctx *pulumi.Context,
	args *MonitoringArgs,
	opts ...pulumi.ResourceOption,
) (err error) {
	// Kubernetes namespace
	mon.ns, err = parts.NewNamespace(ctx, "monitoring", &parts.NamespaceArgs{
		Name: pulumi.String("monitoring"),
		AdditionalLabels: pulumi.StringMap{
			"app.kubernetes.io/part-of": pulumi.String("monitoring"),
			"ctfer.io/stack-name":       pulumi.String(ctx.Stack()),
		},
	}, opts...)
	if err != nil {
		return
	}

	// Create parts of the component
	// => Prometheus, at the root of every others
	mon.prom, err = parts.NewPrometheus(ctx, "prometheus", &parts.PrometheusArgs{
		Namespace: mon.ns.Name,
		Registry:  args.Registry,
	}, opts...)
	if err != nil {
		return
	}

	// => Jaeger to analyze the state of the system
	mon.jaeger, err = parts.NewJaeger(ctx, "jaeger", &parts.JaegerArgs{
		Namespace:     mon.ns.Name,
		PrometheusURL: mon.prom.URL,
		Registry:      args.Registry,
	}, opts...)
	if err != nil {
		return
	}

	// => OTEL Collector to collect all signals
	mon.otel, err = parts.NewOtelCollector(ctx, "otel", &parts.OtelCollectorArgs{
		Namespace:        mon.ns.Name,
		JaegerURL:        mon.jaeger.URL,
		PrometheusURL:    mon.prom.URL,
		ColdExtract:      args.ColdExtract,
		Registry:         args.Registry,
		StorageClassName: args.StorageClassName,
		StorageSize:      args.StorageSize,
		PVCAccessModes:   args.PVCAccessModes,
	}, opts...)
	if err != nil {
		return
	}

	// Isolated NetworkPolicy such that the namespace could be completly isolated by simply
	// shooting out this rule, without affecting its internal services.
	mon.inotelntp, err = netwv1.NewNetworkPolicy(ctx, "in-otel-ntp", &netwv1.NetworkPolicyArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels: pulumi.StringMap{
				"app.kubernetes.io/part-of": pulumi.String("monitoring"),
				"ctfer.io/stack-name":       pulumi.String(ctx.Stack()),
			},
			Namespace: mon.ns.Name,
		},
		Spec: netwv1.NetworkPolicySpecArgs{
			PolicyTypes: pulumi.ToStringArray([]string{
				"Ingress",
			}),
			PodSelector: metav1.LabelSelectorArgs{
				MatchLabels: mon.otel.PodLabels,
			},
			Ingress: netwv1.NetworkPolicyIngressRuleArray{
				// * -> OTEL Collector
				netwv1.NetworkPolicyIngressRuleArgs{
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port: parsePort(mon.otel.Endpoint),
						},
					},
				},
			},
		},
	}, opts...)
	if err != nil {
		return
	}

	// Allow OTEL Collector to send data to Jaeger and Prometheus.
	mon.otelntp, err = netwv1.NewNetworkPolicy(ctx, "otel-ntp", &netwv1.NetworkPolicyArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels: pulumi.StringMap{
				"app.kubernetes.io/part-of": pulumi.String("monitoring"),
				"ctfer.io/stack-name":       pulumi.String(ctx.Stack()),
			},
			Namespace: mon.ns.Name,
		},
		Spec: netwv1.NetworkPolicySpecArgs{
			PolicyTypes: pulumi.ToStringArray([]string{
				"Egress",
			}),
			PodSelector: metav1.LabelSelectorArgs{
				MatchLabels: mon.otel.PodLabels,
			},
			Egress: netwv1.NetworkPolicyEgressRuleArray{
				// OTEL Collector -> Prometheus
				netwv1.NetworkPolicyEgressRuleArgs{
					To: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							NamespaceSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"kubernetes.io/metadata.name": mon.ns.Name,
								},
							},
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: mon.prom.PodLabels,
							},
						},
					},
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port: parseURLPort(mon.prom.URL),
						},
					},
				},
				// OTEL Collector -> Jaeger
				netwv1.NetworkPolicyEgressRuleArgs{
					To: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							NamespaceSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"kubernetes.io/metadata.name": mon.ns.Name,
								},
							},
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: mon.jaeger.PodLabels,
							},
						},
					},
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port: parseURLPort(mon.jaeger.URL),
						},
					},
				},
			},
		},
	}, opts...)
	if err != nil {
		return
	}

	// Allow Jaeger to receive data from OTEL Collector and read data from Prometheus.
	mon.jgrntp, err = netwv1.NewNetworkPolicy(ctx, "jaeger-ntp", &netwv1.NetworkPolicyArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels: pulumi.StringMap{
				"app.kubernetes.io/part-of": pulumi.String("monitoring"),
				"ctfer.io/stack-name":       pulumi.String(ctx.Stack()),
			},
			Namespace: mon.ns.Name,
		},
		Spec: netwv1.NetworkPolicySpecArgs{
			PolicyTypes: pulumi.ToStringArray([]string{
				"Ingress",
				"Egress",
			}),
			PodSelector: metav1.LabelSelectorArgs{
				MatchLabels: mon.jaeger.PodLabels,
			},
			Ingress: netwv1.NetworkPolicyIngressRuleArray{
				// OTEL Collector -> Jaeger
				netwv1.NetworkPolicyIngressRuleArgs{
					From: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							NamespaceSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"kubernetes.io/metadata.name": mon.ns.Name,
								},
							},
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: mon.otel.PodLabels,
							},
						},
					},
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port: parseURLPort(mon.jaeger.URL),
						},
					},
				},
			},
			Egress: netwv1.NetworkPolicyEgressRuleArray{
				// Jaeger -> Prometheus
				netwv1.NetworkPolicyEgressRuleArgs{
					To: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							NamespaceSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"kubernetes.io/metadata.name": mon.ns.Name,
								},
							},
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: mon.prom.PodLabels,
							},
						},
					},
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port: parseURLPort(mon.prom.URL),
						},
					},
				},
			},
		},
	}, opts...)
	if err != nil {
		return
	}

	// Allow Prometheus to receive traffic from the OTEL Collector and Jaeger.
	mon.promntp, err = netwv1.NewNetworkPolicy(ctx, "prom-ntp", &netwv1.NetworkPolicyArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels: pulumi.StringMap{
				"app.kubernetes.io/part-of": pulumi.String("monitoring"),
				"ctfer.io/stack-name":       pulumi.String(ctx.Stack()),
			},
			Namespace: mon.ns.Name,
		},
		Spec: netwv1.NetworkPolicySpecArgs{
			PolicyTypes: pulumi.ToStringArray([]string{
				"Ingress",
			}),
			PodSelector: metav1.LabelSelectorArgs{
				MatchLabels: mon.prom.PodLabels,
			},
			Ingress: netwv1.NetworkPolicyIngressRuleArray{
				// OTEL Collector -> Prometheus
				netwv1.NetworkPolicyIngressRuleArgs{
					From: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							NamespaceSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"kubernetes.io/metadata.name": mon.ns.Name,
								},
							},
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: mon.otel.PodLabels,
							},
						},
					},
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port: parseURLPort(mon.prom.URL),
						},
					},
				},
				// Jaeger -> Prometheus
				netwv1.NetworkPolicyIngressRuleArgs{
					From: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							NamespaceSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"kubernetes.io/metadata.name": mon.ns.Name,
								},
							},
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: mon.jaeger.PodLabels,
							},
						},
					},
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port: parseURLPort(mon.prom.URL),
						},
					},
				},
			},
		},
	}, opts...)

	return
}

func (mon *Monitoring) outputs(ctx *pulumi.Context) (err error) {
	mon.Namespace = mon.ns.Name
	mon.OTEL.Endpoint = mon.otel.Endpoint
	mon.OTEL.ColdExtractPVCName = mon.otel.ColdExtractPVCName
	mon.OTEL.PodLabels = mon.otel.PodLabels

	return ctx.RegisterResourceOutputs(mon, pulumi.Map{
		"namespace":               mon.Namespace,
		"otel.endpoint":           mon.OTEL.Endpoint,
		"otel.coldExtractPVCName": mon.OTEL.ColdExtractPVCName,
		"otel.podLabels":          mon.OTEL.PodLabels,
	})
}

// parsePort cuts the input endpoint to return its port.
// Example: some.thing:port -> port
func parsePort(edp pulumi.StringInput) pulumi.IntOutput {
	return edp.ToStringOutput().ApplyT(func(edp string) (int, error) {
		_, pStr, _ := strings.Cut(edp, ":")
		p, err := strconv.Atoi(pStr)
		if err != nil {
			return 0, errors.Wrapf(err, "parsing endpoint %s for port", edp)
		}
		return p, nil
	}).(pulumi.IntOutput)
}

// parseURLPort parses the input endpoint formatted as a URL to return its port.
// Example: http://some.thing:port -> port
func parseURLPort(edp pulumi.StringOutput) pulumi.IntOutput {
	return edp.ToStringOutput().ApplyT(func(edp string) (int, error) {
		u, err := url.Parse(edp)
		if err != nil {
			return 0, errors.Wrapf(err, "parsing endpoint %s as a URL", edp)
		}
		p, err := strconv.Atoi(u.Port())
		if err != nil {
			return 0, errors.Wrapf(err, "parsing endpoint %s for port", edp)
		}
		return p, nil
	}).(pulumi.IntOutput)
}
