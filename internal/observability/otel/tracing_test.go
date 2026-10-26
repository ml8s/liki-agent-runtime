package otel

import (
	"context"
	"testing"
	"time"
)

func TestSamplerFromEnvAllModes(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "")
	if name, ratio := samplerRatio(); name != "parentbased_traceidratio" || ratio != 1 {
		t.Fatalf("default sampler = %q/%v", name, ratio)
	}

	t.Setenv("OTEL_TRACES_SAMPLER", "Always_Off")
	if name := samplerName(); name != "always_off" {
		t.Fatalf("sampler name = %q", name)
	}

	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.25")
	if name, ratio := samplerRatio(); name != "always_off" || ratio != 0.25 {
		t.Fatalf("sampler ratio = %q/%v", name, ratio)
	}

	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "bad")
	if _, ratio := samplerRatio(); ratio != 1 {
		t.Fatalf("invalid ratio = %v, want fallback 1", ratio)
	}
}

func TestShutdownWithTimeout(t *testing.T) {
	ctx := t.Context()
	provider, err := NewTracerProvider(ctx, "test", "test", "test")
	if err != nil {
		t.Fatalf("NewTracerProvider() error = %v", err)
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if err := provider.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestTracerProviderNoEndpoint(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	provider, err := NewTracerProvider(t.Context(), "test", "1.0", "test")
	if err != nil {
		t.Fatalf("NewTracerProvider() error = %v", err)
	}
	if provider.Provider() == nil {
		t.Fatal("Provider() = nil, want noop provider")
	}
	if provider.Tracer("test") == nil {
		t.Fatal("Tracer() = nil, want noop tracer")
	}
	if err := provider.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestTracerProviderSDKDisabled(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "true")
	provider, err := NewTracerProvider(t.Context(), "test", "1.0", "test")
	if err != nil {
		t.Fatalf("NewTracerProvider() error = %v", err)
	}
	if provider.Provider() == nil {
		t.Fatal("Provider() = nil for disabled SDK")
	}
	if err := provider.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestTracerProviderNilSafety(t *testing.T) {
	var provider *TracerProvider
	if tracer := provider.Tracer("test"); tracer == nil {
		t.Fatal("nil Tracer() = nil, want noop tracer")
	}
	if err := provider.Shutdown(t.Context()); err != nil {
		t.Fatalf("nil Shutdown() error = %v", err)
	}
	if p := provider.Provider(); p != nil {
		t.Fatalf("nil Provider() = %v, want nil", p)
	}
}

func TestSamplerFromEnvTable(t *testing.T) {
	testCases := []struct {
		sampler string
	}{
		{sampler: "always_on"},
		{sampler: "always_off"},
		{sampler: "traceidratio"},
		{sampler: "parentbased_always_on"},
		{sampler: "parentbased_always_off"},
		{sampler: "parentbased_traceidratio"},
		{sampler: "unknown_sampler"},
		{sampler: ""},
	}
	for _, testCase := range testCases {
		t.Run(testCase.sampler, func(t *testing.T) {
			t.Setenv("OTEL_TRACES_SAMPLER", testCase.sampler)
			t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.5")
			s := samplerFromEnv()
			if s == nil {
				t.Fatal("samplerFromEnv() = nil")
			}
		})
	}
}

func TestSamplerRatioFallback(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "-1")
	if _, ratio := samplerRatio(); ratio != 1 {
		t.Fatalf("negative ratio = %v, want fallback 1", ratio)
	}

	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "1.5")
	if _, ratio := samplerRatio(); ratio != 1 {
		t.Fatalf("ratio > 1 = %v, want fallback 1", ratio)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "value", "other"); got != "value" {
		t.Fatalf("firstNonEmpty() = %q, want value", got)
	}
	if got := firstNonEmpty("", "  ", ""); got != "" {
		t.Fatalf("firstNonEmpty() = %q, want empty", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Fatalf("firstNonEmpty() = %q, want empty", got)
	}
}

func TestInstallGlobalPropagator(t *testing.T) {
	InstallGlobalPropagator()
	if Propagator() == nil {
		t.Fatal("Propagator() = nil after InstallGlobalPropagator()")
	}
}
