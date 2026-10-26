// Package otel wires OpenTelemetry tracing using standard SDK environment
// configuration. It does not define a private telemetry protocol.
package otel

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// TracerProvider owns lifecycle for the process-global tracer provider.
type TracerProvider struct {
	provider *sdktrace.TracerProvider
	noop     trace.TracerProvider
	shutdown func(context.Context) error
	enabled  bool
}

// NewTracerProvider initializes OTLP trace export when standard OTLP endpoint
// environment is present. Otherwise it installs the standard no-op provider.
func NewTracerProvider(ctx context.Context, serviceName, serviceVersion, environment string) (*TracerProvider, error) {
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		noopProvider := tracenoop.NewTracerProvider()
		otel.SetTracerProvider(noopProvider)
		return &TracerProvider{noop: noopProvider, shutdown: func(context.Context) error { return nil }}, nil
	}

	endpoint := firstNonEmpty(
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"),
		os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
	)
	if endpoint == "" {
		noopProvider := tracenoop.NewTracerProvider()
		otel.SetTracerProvider(noopProvider)
		return &TracerProvider{noop: noopProvider, shutdown: func(context.Context) error { return nil }}, nil
	}

	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create OTLP HTTP trace exporter: %w", err)
	}

	environmentResource, err := resource.New(
		ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
			semconv.DeploymentEnvironmentNameKey.String(environment),
		),
		resource.WithSchemaURL(semconv.SchemaURL),
	)
	if err != nil {
		return nil, fmt.Errorf("create telemetry environment resource: %w", err)
	}
	resource, err := resource.Merge(
		resource.Default(),
		environmentResource,
	)
	if err != nil {
		return nil, fmt.Errorf("merge telemetry resources: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(samplerFromEnv()),
	)
	otel.SetTracerProvider(provider)
	return &TracerProvider{
		provider: provider,
		shutdown: provider.Shutdown,
		enabled:  true,
	}, nil
}

// Tracer returns a named tracer from the active provider.
func (p *TracerProvider) Tracer(name string, options ...trace.TracerOption) trace.Tracer {
	if p == nil || p.provider == nil {
		return tracenoop.NewTracerProvider().Tracer(name)
	}
	return p.provider.Tracer(name, options...)
}

// Shutdown flushes and closes the trace provider.
func (p *TracerProvider) Shutdown(ctx context.Context) error {
	if p == nil || p.shutdown == nil {
		return nil
	}
	return p.shutdown(ctx)
}

// Provider returns the standard tracer provider, or a no-op when tracing is
// disabled.
func (p *TracerProvider) Provider() trace.TracerProvider {
	if p == nil {
		return nil
	}
	if p.provider == nil {
		return p.noop
	}
	return p.provider
}

func samplerFromEnv() sdktrace.Sampler {
	name := samplerName()
	_, ratio := samplerRatio()
	based := sdktrace.TraceIDRatioBased(ratio)
	switch name {
	case "always_on":
		return sdktrace.AlwaysSample()
	case "always_off":
		return sdktrace.NeverSample()
	case "traceidratio":
		return based
	case "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample())
	case "parentbased_traceidratio":
		return sdktrace.ParentBased(based)
	default:
		return sdktrace.ParentBased(based)
	}
}

func samplerName() string {
	if name := strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER"))); name != "" {
		return name
	}
	return "parentbased_traceidratio"
}

func samplerRatio() (string, float64) {
	raw := strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER_ARG"))
	if raw == "" {
		return "parentbased_traceidratio", 1
	}
	ratio, err := strconv.ParseFloat(raw, 64)
	if err != nil || ratio < 0 || ratio > 1 {
		return samplerName(), 1
	}
	return samplerName(), ratio
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// InstallGlobalPropagator uses the W3C standard propagation formats.
func InstallGlobalPropagator() {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

// Propagator returns the W3C trace-context and baggage propagator.
func Propagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}
