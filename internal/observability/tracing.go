package observability

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// SetupTracing initialises the global OTel trace provider with an OTLP/HTTP
// exporter pointing at endpoint. If endpoint is empty, tracing is disabled and
// a no-op shutdown is returned so callers need not special-case the env var.
// Accepts both full URLs (http://host:port/path) via WithEndpointURL and
// host:port via WithEndpoint; scheme in the URL determines TLS usage.
func SetupTracing(ctx context.Context, endpoint string) (func(context.Context) error, error) {
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	opts := []otlptracehttp.Option{}
	if strings.Contains(endpoint, "://") {
		// Full URL: use WithEndpointURL; TLS is determined by the scheme.
		opts = append(opts, otlptracehttp.WithEndpointURL(endpoint))
	} else {
		// host:port only: use WithEndpoint and explicit WithInsecure.
		opts = append(opts, otlptracehttp.WithEndpoint(endpoint), otlptracehttp.WithInsecure())
	}

	exp, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("observability: OTLP exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName("puls-server")),
	)
	if err != nil {
		return nil, fmt.Errorf("observability: build resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}
