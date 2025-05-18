package main

import (
	"github.com/ctfer-io/monitoring/services"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg := config.New(ctx, "")

		mon, err := services.NewMonitoring(ctx, "monitoring", &services.MonitoringArgs{
			ColdExtract: cfg.GetBool("cold-extract"),
			Registry:    pulumi.StringPtr(cfg.Get("registry")),
		})
		if err != nil {
			return err
		}

		ctx.Export("namespace", mon.Namespace)
		ctx.Export("otel.endpoint", mon.OTEL.Endpoint)
		ctx.Export("otel.cold-extract.signals-pvc-name", mon.OTEL.ColdExtractPVCName)
		return nil
	})
}
