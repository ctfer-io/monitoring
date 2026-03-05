package extract

import "go.uber.org/zap"

type options struct {
	logger   *zap.Logger
	registry string
}

// Option is the interface for all extraction-related functional options.
type Option interface {
	apply(*options)
}

type loggerOption struct {
	logger *zap.Logger
}

func (opt loggerOption) apply(opts *options) {
	opts.logger = opt.logger
}

// WithLogger provides a zap logger to detail ongoing efforts through the extraction phase.
func WithLogger(logger *zap.Logger) Option {
	return loggerOption{logger: logger}
}

type registryOption string

func (opt registryOption) apply(opts *options) {
	opts.registry = string(opt)
}

// WithRegistry provides an optional registry in which to look for the Busybox image.
//
// Useful for air-gap environments or when working with an OCI cache/proxy.
func WithRegistry(registry string) Option {
	return registryOption(registry)
}
