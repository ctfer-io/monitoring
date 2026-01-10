package parts

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/pkg/errors"
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	helmv4 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v4"
	v1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"go.uber.org/multierr"
)

type (
	Perses struct {
		pulumi.ResourceState

		chart    *helmv4.Chart
		globalDS *corev1.ConfigMap

		PodLabels pulumi.StringMapOutput
	}

	PersesArgs struct {
		// Global attributes

		Namespace pulumi.StringInput

		Registry pulumi.StringInput
		registry pulumi.StringOutput

		// Prometheus-related attributes

		// If no Prometheus URL is defined, there will be no data to display,
		// hence is required.
		PrometheusURL pulumi.StringInput
	}
)

func NewPerses(ctx *pulumi.Context, name string, args *PersesArgs, opts ...pulumi.ResourceOption) (*Perses, error) {
	prs := &Perses{}

	args = prs.defaults(args)
	if err := prs.check(args); err != nil {
		return nil, err
	}
	if err := ctx.RegisterComponentResource("ctfer-io:monitoring:perses", name, prs, opts...); err != nil {
		return nil, err
	}
	opts = append(opts, pulumi.Parent(prs))
	if err := prs.provision(ctx, args, opts...); err != nil {
		return nil, err
	}
	if err := prs.outputs(ctx); err != nil {
		return nil, err
	}

	return prs, nil
}

func (*Perses) defaults(args *PersesArgs) *PersesArgs {
	if args == nil {
		args = &PersesArgs{}
	}

	// Define private registry if any
	args.registry = pulumi.String("docker.io").ToStringOutput()
	if args.Registry != nil {
		args.registry = args.Registry.ToStringPtrOutput().ApplyT(func(in *string) string {
			// No private registry -> defaults to Docker Hub
			if in == nil || *in == "" {
				return "docker.io"
			}

			// If one set, remove it as charts already does it
			return strings.TrimSuffix(*in, "/")
		}).(pulumi.StringOutput)
	}

	return args
}

func (prs *Perses) check(args *PersesArgs) error {
	if args.PrometheusURL == nil {
		return errors.New("no prometheus URL configured")
	}

	wg := &sync.WaitGroup{}
	checks := 1 // number of checks to perform
	wg.Add(checks)
	cerr := make(chan error, checks)

	args.PrometheusURL.ToStringOutput().ApplyT(func(url string) error {
		defer wg.Done()

		if url == "" {
			cerr <- errors.New("empty prometheus URL configured")
		}
		return nil
	})

	wg.Wait()
	close(cerr)

	var merr error
	for err := range cerr {
		merr = multierr.Append(merr, err)
	}
	return merr
}

func (prs *Perses) provision(ctx *pulumi.Context, args *PersesArgs, opts ...pulumi.ResourceOption) (err error) {
	prs.chart, err = helmv4.NewChart(ctx, "perses", &helmv4.ChartArgs{
		Chart: pulumi.String("perses"),
		RepositoryOpts: helmv4.RepositoryOptsArgs{
			Repo: pulumi.String("https://perses.github.io/helm-charts"),
		},
		Version:   pulumi.String("0.19.2"),
		Namespace: args.Namespace,
		Values: pulumi.Map{
			"image": pulumi.Map{
				"registry": args.registry,
			},
			"sidecar": pulumi.Map{
				// Watch for ConfigMaps with perses.dev/resource=true in all namespaces,
				// so other services' dashboard can be automatically discovered.
				"enabled":       pulumi.Bool(true),
				"label":         pulumi.String("perses.dev/resource"),
				"labelValue":    pulumi.String("true"),
				"allNamespaces": pulumi.Bool(true),
			},
			"config": pulumi.Map{
				"provisioning": pulumi.Map{
					// During bootstrap we intensively deploy things, so we need faster than default 10m
					"interval": pulumi.String("1m"),
				},
			},
		},
	}, opts...)
	if err != nil {
		return
	}

	prs.globalDS, err = corev1.NewConfigMap(ctx, "global-datasource", &corev1.ConfigMapArgs{
		Metadata: v1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"app.kubernetes.io/name":      pulumi.String("perses"),
				"app.kubernetes.io/component": pulumi.String("perses"),
				"app.kubernetes.io/part-of":   pulumi.String("monitoring"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
				"perses.dev/resource":         pulumi.String("true"), // Get discovered by Perses
			},
		},
		Data: pulumi.StringMap{
			"global-datasource.json": func() pulumi.StringOutput {
				// Build the manifest to configure the default global datasource, pointing to
				// a Prometheus-compatible query-able endpoint.
				// References:
				// - https://perses.dev/perses/docs/api/datasource/
				// - https://perses.dev/plugins/docs/prometheus/model/#prometheusdatasource
				return pulumi.Map{
					"kind": pulumi.String("GlobalDatasource"),
					"metadata": pulumi.Map{
						"name": pulumi.String("prometheus-datasource"),
					},
					"spec": pulumi.Map{
						"default": pulumi.Bool(true),
						"plugin": pulumi.Map{
							"kind": pulumi.String("PrometheusDatasource"),
							"spec": pulumi.Map{
								"directUrl": args.PrometheusURL,
							},
						},
					},
				}.ToMapOutput().ApplyT(func(data any) string {
					b, err := json.Marshal(data)
					if err != nil {
						panic(err) // should not happen, we control all this
					}
					return string(b)
				}).(pulumi.StringOutput)
			}(),
		},
	}, opts...)
	if err != nil {
		return
	}

	return
}

func (prs *Perses) outputs(ctx *pulumi.Context) error {
	prs.PodLabels = prs.chart.Resources.ApplyT(func(res []any) (labels pulumi.StringMapOutput) {
		for _, r := range res {
			svc, ok := r.(*appsv1.StatefulSet)
			if !ok {
				continue
			}
			return svc.Spec.Template().Metadata().Labels()
		}
		return
	}).(pulumi.StringMapOutput)

	return ctx.RegisterResourceOutputs(prs, pulumi.Map{
		"podLabels": prs.PodLabels,
	})
}
