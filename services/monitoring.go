package services

import (
	"github.com/ctfer-io/monitoring/services/parts"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type (
	Monitoring struct {
		pulumi.ResourceState

		// Global resources
		ns *corev1.Namespace

		// Subcomponents
		otel   *parts.OtelCollector
		jaeger *parts.Jaeger
		prom   *parts.Prometheus

		// Outputs
		Namespace pulumi.StringOutput
		OTEL      MonitoringOTEL
	}

	MonitoringOTEL struct {
		Endpoint           pulumi.StringOutput
		ColdExtractPVCName pulumi.StringPtrOutput
	}

	MonitoringArgs struct {
		Registry         pulumi.StringInput
		StorageClassName pulumi.StringInput
		StorageSize      pulumi.StringInput
		PVCAccessModes   pulumi.StringArrayInput

		ColdExtract bool
	}
)

func NewMonitoring(ctx *pulumi.Context, name string, args *MonitoringArgs, opts ...pulumi.ResourceOption) (*Monitoring, error) {
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

func (mon *Monitoring) provision(ctx *pulumi.Context, args *MonitoringArgs, opts ...pulumi.ResourceOption) (err error) {
	// Kubernetes namespace
	mon.ns, err = corev1.NewNamespace(ctx, "monitoring", &corev1.NamespaceArgs{}, opts...)
	if err != nil {
		return
	}

	// Create parts of the component
	// => Prometheus, at the root of every others
	mon.prom, err = parts.NewPrometheus(ctx, "prometheus", &parts.PrometheusArgs{
		Namespace: mon.ns.Metadata.Name().Elem(),
		Registry:  args.Registry,
	}, opts...)
	if err != nil {
		return
	}

	// => Jaeger to analyze the state of the system
	mon.jaeger, err = parts.NewJaeger(ctx, "jaeger", &parts.JaegerArgs{
		Namespace:     mon.ns.Metadata.Name().Elem(),
		PrometheusURL: mon.prom.URL,
		Registry:      args.Registry,
	}, opts...)
	if err != nil {
		return
	}

	// => OpenTelemetry to collect all signals
	mon.otel, err = parts.NewOtelCollector(ctx, "otel", &parts.OtelCollectorArgs{
		Namespace:        mon.ns.Metadata.Name().Elem(),
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

	return
}

func (mon *Monitoring) outputs(ctx *pulumi.Context) (err error) {
	mon.Namespace = mon.ns.Metadata.Name().Elem()
	mon.OTEL.Endpoint = pulumi.Sprintf("http://%s", mon.otel.Endpoint)
	mon.OTEL.ColdExtractPVCName = mon.otel.ColdExtractPVCName

	return ctx.RegisterResourceOutputs(mon, pulumi.Map{
		"namespace":               mon.Namespace,
		"otel.endpoint":           mon.OTEL.Endpoint,
		"otel.coldExtractPVCName": mon.OTEL.ColdExtractPVCName,
	})
}
