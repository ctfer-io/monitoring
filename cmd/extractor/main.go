package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/ctfer-io/monitoring/pkg/extract"
	"github.com/urfave/cli/v3"
	"go.uber.org/zap"
)

var (
	Version = "dev"
	Commit  = ""
	Date    = ""
	BuiltBy = ""

	logger     *zap.Logger
	loggerOnce sync.Once
)

const (
	img = "library/busybox:1.37.0"
)

func main() {
	app := &cli.Command{
		Name:  "Monitoring Extractor",
		Usage: "Extract the Monitoring files from an OpenTelemetry Collector.",
		Flags: []cli.Flag{
			cli.VersionFlag,
			cli.HelpFlag,
			&cli.StringFlag{
				Name:     "namespace",
				Sources:  cli.EnvVars("NAMESPACE"),
				Required: true,
				Usage:    "The namespace in which to deploy the extraction Pod.",
			},
			&cli.StringFlag{
				Name:     "pvc-name",
				Sources:  cli.EnvVars("PVC_NAME"),
				Required: true,
				Usage:    "The PVC name to mount and copy files from.",
			},
			&cli.StringFlag{
				Name:     "directory",
				Sources:  cli.EnvVars("DIRECTORY"),
				Required: true,
				Usage:    "The directory in which to export the OpenTelemetry Collector files.",
			},
			&cli.StringFlag{
				Name:    "registry",
				Sources: cli.EnvVars("REGISTRY"),
				Usage:   "An optional OCI registry from which to pool the Docker image used to extract the files (" + img + ").",
			},
		},
		Action: run,
		Authors: []any{
			"CTFer.io Authors & Contributors - ctfer-io@protonmail.com",
		},
		Version: Version,
		Metadata: map[string]any{
			"version": Version,
			"commit":  Commit,
			"date":    Date,
			"builtBy": BuiltBy,
		},
	}

	// Create context that listens for the interrupt signal from the OS.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, os.Args); err != nil {
		log().Fatal("fatal error",
			zap.Error(err),
		)
		os.Exit(1)
	}
}

func run(ctx context.Context, cmd *cli.Command) error {
	return extract.DumpOTelCollector(ctx,
		cmd.String("namespace"),
		cmd.String("pvc-name"),
		cmd.String("directory"),
		extract.WithLogger(log()),
		extract.WithRegistry(cmd.String("registry")), // deal with empty string, don't worry ;)
	)
}

func log() *zap.Logger {
	loggerOnce.Do(func() {
		logger, _ = zap.NewProduction()
	})
	return logger
}
