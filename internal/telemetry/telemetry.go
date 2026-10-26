// Package telemetry configures process-wide OpenTelemetry providers.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	defaultSampleRatio = 0.1
	exportTimeout      = 5 * time.Second
)

// Setup configures optional OTLP exporters. Exporting remains disabled unless
// an OTEL_EXPORTER_OTLP_* endpoint is set, so local development needs no
// collector. The returned function must be called during graceful shutdown.
func Setup(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	if ctx == nil {
		return nil, errors.New("telemetry setup context is required")
	}
	if strings.TrimSpace(serviceName) == "" {
		return nil, errors.New("telemetry service name is required")
	}

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Error("OpenTelemetry error", "error", err)
	}))

	tracesEnabled := hasAnyEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	metricsEnabled := hasAnyEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")
	if !tracesEnabled && !metricsEnabled {
		return func(context.Context) error { return nil }, nil
	}

	res, err := resource.New(ctx, resource.WithAttributes(attribute.String("service.name", serviceName)))
	if err != nil {
		return nil, fmt.Errorf("create telemetry resource: %w", err)
	}

	var tracerProvider *sdktrace.TracerProvider
	if tracesEnabled {
		sampler, err := configuredSampler()
		if err != nil {
			return nil, err
		}
		exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithTimeout(exportTimeout))
		if err != nil {
			return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
		}
		tracerProvider = sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sampler),
			sdktrace.WithBatcher(exporter,
				sdktrace.WithMaxQueueSize(2048),
				sdktrace.WithMaxExportBatchSize(512),
				sdktrace.WithBatchTimeout(5*time.Second),
			),
		)
	}

	var meterProvider *metric.MeterProvider
	if metricsEnabled {
		exporter, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithTimeout(exportTimeout))
		if err != nil {
			exporterErr := fmt.Errorf("create OTLP metric exporter: %w", err)
			if tracerProvider != nil {
				if shutdownErr := tracerProvider.Shutdown(ctx); shutdownErr != nil {
					exporterErr = errors.Join(exporterErr, fmt.Errorf("shutdown trace provider after setup failure: %w", shutdownErr))
				}
			}
			return nil, exporterErr
		}
		meterProvider = metric.NewMeterProvider(
			metric.WithResource(res),
			metric.WithReader(metric.NewPeriodicReader(exporter,
				metric.WithInterval(30*time.Second),
				metric.WithTimeout(exportTimeout),
			)),
		)
	}

	if tracerProvider != nil {
		otel.SetTracerProvider(tracerProvider)
	}
	if meterProvider != nil {
		otel.SetMeterProvider(meterProvider)
	}

	return func(shutdownCtx context.Context) error {
		if shutdownCtx == nil {
			return errors.New("telemetry shutdown context is required")
		}
		var shutdownErrs []error
		if meterProvider != nil {
			if err := meterProvider.Shutdown(shutdownCtx); err != nil {
				shutdownErrs = append(shutdownErrs, fmt.Errorf("shutdown metric provider: %w", err))
			}
		}
		if tracerProvider != nil {
			if err := tracerProvider.Shutdown(shutdownCtx); err != nil {
				shutdownErrs = append(shutdownErrs, fmt.Errorf("shutdown trace provider: %w", err))
			}
		}
		return errors.Join(shutdownErrs...)
	}, nil
}

func hasAnyEnv(keys ...string) bool {
	for _, key := range keys {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

func configuredSampler() (sdktrace.Sampler, error) {
	name := strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER"))
	if name == "" {
		name = "parentbased_traceidratio"
	}
	switch name {
	case "always_on":
		return sdktrace.AlwaysSample(), nil
	case "always_off":
		return sdktrace.NeverSample(), nil
	case "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample()), nil
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample()), nil
	case "traceidratio", "parentbased_traceidratio":
	default:
		return nil, fmt.Errorf("unsupported OTEL_TRACES_SAMPLER %q", name)
	}
	arg := strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER_ARG"))
	if arg == "" {
		arg = strconv.FormatFloat(defaultSampleRatio, 'f', -1, 64)
	}
	ratio, err := strconv.ParseFloat(arg, 64)
	if err != nil || ratio < 0 || ratio > 1 {
		return nil, errors.New("OTEL_TRACES_SAMPLER_ARG must be a number between 0 and 1")
	}
	traceIDSampler := sdktrace.TraceIDRatioBased(ratio)
	switch name {
	case "traceidratio":
		return traceIDSampler, nil
	case "parentbased_traceidratio":
		return sdktrace.ParentBased(traceIDSampler), nil
	}
	return nil, fmt.Errorf("unsupported OTEL_TRACES_SAMPLER %q", name)
}
