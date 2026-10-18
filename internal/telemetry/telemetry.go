// Package telemetry sets up OpenTelemetry tracing. Off unless OTEL_EXPORTER_OTLP_ENDPOINT
// (or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT) is set; then spans go out over OTLP/HTTP, with
// the standard OTEL_* variables (headers, sampler) honoured by the exporter.
package telemetry

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Tracer is the package-wide tracer (a no-op until Setup installs a provider).
func Tracer() trace.Tracer { return otel.Tracer("github.com/hyunsuhahaha/public-api-monitor") }

// Enabled reports whether an OTLP endpoint is configured.
func Enabled() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

// Setup installs the tracer provider; call the returned function on shutdown to flush.
func Setup(ctx context.Context, role, location string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if !Enabled() {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	res := resource.NewSchemaless(
		attribute.String("service.name", "pamon"),
		attribute.String("service.instance.id", location),
		attribute.String("pamon.role", role),
	)
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}
