package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Aspire injects the OTLP endpoint, credentials and resource identity. Exporters
// read those standard variables; no dashboard URL/key is stored in source.
func setupTelemetry(ctx context.Context) (func(context.Context) error, error) {
	console := slog.NewTextHandler(os.Stderr, nil)
	slog.SetDefault(slog.New(console))
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	var shutdowns []func(context.Context) error
	shutdown := func(ctx context.Context) error {
		var errs []error
		for _, close := range shutdowns {
			errs = append(errs, close(ctx))
		}
		return errors.Join(errs...)
	}
	fail := func(err error) (func(context.Context) error, error) {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return nil, errors.Join(err, shutdown(cleanup))
	}
	res := resource.Default()
	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return fail(fmt.Errorf("trace exporter: %w", err))
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(traceExporter))
	shutdowns = append(shutdowns, tp.Shutdown)
	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return fail(fmt.Errorf("metric exporter: %w", err))
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(5*time.Second))))
	shutdowns = append(shutdowns, mp.Shutdown)
	logExporter, err := otlploggrpc.New(ctx)
	if err != nil {
		return fail(fmt.Errorf("log exporter: %w", err))
	}
	lp := sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)))
	shutdowns = append(shutdowns, lp.Shutdown)
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	slog.SetDefault(slog.New(slog.NewMultiHandler(console, otelslog.NewHandler("vector-api", otelslog.WithLoggerProvider(lp)))))
	return shutdown, nil
}
