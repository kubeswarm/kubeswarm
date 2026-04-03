/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package observability initialises OpenTelemetry for the operator and agent
// runtime. Call Init (cluster mode, OTLP export) or InitStdout (local dev,
// pretty-print to stdout). Both return a shutdown function that must be
// deferred by the caller.
//
// When OTEL_EXPORTER_OTLP_ENDPOINT is empty and Init is called, a no-op
// provider is installed — safe for local dev and unit tests.
package observability

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const (
	// EnvOTLPEndpoint is the standard OTel env var for the OTLP collector endpoint.
	EnvOTLPEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"
)

// Init sets up the global TracerProvider and MeterProvider for cluster mode.
// If OTEL_EXPORTER_OTLP_ENDPOINT is empty, no-op providers are installed and
// the returned shutdown is a no-op — no collector required.
//
// Wires W3C TraceContext propagation globally so trace context flows through
// task.Meta["traceparent"] across Redis queue boundaries.
func Init(ctx context.Context, serviceName string) (shutdown func(), err error) {
	if os.Getenv(EnvOTLPEndpoint) == "" {
		installNoop()
		return func() {}, nil
	}

	res, err := buildResource(ctx, serviceName)
	if err != nil {
		return func() {}, err
	}

	// Trace provider — OTLP HTTP exporter.
	traceExp, err := otlptracehttp.New(ctx)
	if err != nil {
		return func() {}, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)

	// Metric provider — OTLP HTTP exporter.
	metricExp, err := otlpmetrichttp.New(ctx)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return func() {}, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)

	install(tp, mp)

	return func() {
		_ = tp.Shutdown(context.Background())
		_ = mp.Shutdown(context.Background())
	}, nil
}

// InitStdout sets up a stdout trace exporter for local `swarm run` mode.
// Spans are written to stdout as JSON as they complete.
// The returned shutdown must be deferred — it flushes any pending spans.
// Metrics are no-op in stdout mode (not useful for single local runs).
func InitStdout(ctx context.Context, serviceName string) (shutdown func(), err error) {
	res, err := buildResource(ctx, serviceName)
	if err != nil {
		return func() {}, err
	}

	exp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		return func() {}, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	install(tp, metricnoop.NewMeterProvider())

	return func() { _ = tp.Shutdown(context.Background()) }, nil
}

// Tracer returns the global OTel tracer for the given instrumentation scope.
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

// Meter returns the global OTel meter for the given instrumentation scope.
func Meter(name string) metric.Meter {
	return otel.GetMeterProvider().Meter(name)
}

// install registers tp and mp as the global providers and sets W3C propagation.
func install(tp *sdktrace.TracerProvider, mp metric.MeterProvider) {
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

// installNoop registers no-op providers — all OTel calls become zero-cost stubs.
func installNoop() {
	otel.SetTracerProvider(tracenoop.NewTracerProvider())
	otel.SetMeterProvider(metricnoop.NewMeterProvider())
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

func buildResource(ctx context.Context, serviceName string) (*resource.Resource, error) {
	return resource.New(ctx,
		resource.WithAttributes(semconv.ServiceNameKey.String(serviceName)),
		resource.WithProcess(),
		resource.WithOS(),
	)
}
