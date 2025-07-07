package main

import (
	"github.com/ctfer-io/monitoring/services"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg := loadConfig(ctx)

		mon, err := services.NewMonitoring(ctx, "monitoring", &services.MonitoringArgs{
			ColdExtract:      cfg.ColdExtract,
			Registry:         pulumi.String(cfg.Registry),
			StorageClassName: pulumi.String(cfg.StorageClassName),
			StorageSize:      pulumi.String(cfg.StorageSize),
			PVCAccessModes: pulumi.ToStringArray([]string{
				cfg.PVCAccessMode,
			}),
		})
		if err != nil {
			return err
		}

		ctx.Export("namespace", mon.Namespace)
		ctx.Export("otel-endpoint", mon.OTEL.Endpoint)
		ctx.Export("otel-cold-extract-pvc-name", mon.OTEL.ColdExtractPVCName)

		return nil
	})
}

type Config struct {
	ColdExtract      bool
	Registry         string
	StorageClassName string
	StorageSize      string
	PVCAccessMode    string
}

func loadConfig(ctx *pulumi.Context) *Config {
	cfg := config.New(ctx, "monitoring")
	return &Config{
		ColdExtract:      cfg.GetBool("cold-extract"),
		Registry:         cfg.Get("registry"),
		StorageClassName: cfg.Get("storage-class-name"),
		StorageSize:      cfg.Get("storage-size"),
		PVCAccessMode:    cfg.Get("pvc-access-mode"),
	}
}
