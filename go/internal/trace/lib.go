package trace

import (
	"context"
	"errors"
	"runtime"

	"github.com/grafana/pyroscope-go"
	"go.opentelemetry.io/otel"
	attr "go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.25.0"

	tr "go.opentelemetry.io/otel/trace"
)

// setupOTelSDK bootstraps the OpenTelemetry pipeline.
// If it does not return an error, make sure to call shutdown for proper cleanup.
func SetupOTelSDK(ctx context.Context) (shutdown func(context.Context) error, err error) {
	var shutdownFuncs []func(context.Context) error

	// shutdown calls cleanup functions registered via shutdownFuncs.
	// The errors from the calls are joined.
	// Each registered cleanup will be invoked once.
	shutdown = func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	// handleErr calls shutdown for cleanup and makes sure that all errors are returned.
	handleErr := func(inErr error) {
		err = errors.Join(inErr, shutdown(ctx))
	}

	// Set up propagator.
	prop := newPropagator()
	otel.SetTextMapPropagator(prop)

	// Set up trace provider.
	tracerProvider, err := newTraceProvider(ctx)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, tracerProvider.ForceFlush, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	// Set up logger provider.
	loggerProvider, err := newLoggerProvider(ctx)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, loggerProvider.ForceFlush, loggerProvider.Shutdown)
	global.SetLoggerProvider(loggerProvider)

	return
}

func newPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

func newTraceProvider(ctx context.Context) (*trace.TracerProvider, error) {
	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	traceRes, err := resource.New(ctx,
		resource.WithOS(),
		resource.WithAttributes(semconv.ServiceNameKey.String("ayup")),
	)
	if err != nil {
		return nil, err
	}

	traceProvider := trace.NewTracerProvider(
		trace.WithResource(traceRes),
		trace.WithSyncer(traceExporter),
		// trace.WithBatcher(traceExporter,
		// 	trace.WithBatchTimeout(time.Millisecond*10)),
	)
	return traceProvider, nil
}

func newLoggerProvider(ctx context.Context) (*log.LoggerProvider, error) {
	logExporter, err := otlploghttp.New(ctx, otlploghttp.WithInsecure())
	if err != nil {
		return nil, err
	}

	loggerProvider := log.NewLoggerProvider(
		log.WithProcessor(log.NewBatchProcessor(logExporter)),
	)
	return loggerProvider, nil
}

func SetupPyroscopeProfiling(endpoint string) {
	if endpoint == "" {
		return
	}

	runtime.SetMutexProfileFraction(1)
	runtime.SetBlockProfileRate(1)

	_, _ = pyroscope.Start(pyroscope.Config{
		ApplicationName: "premai.io.ayup",
		ServerAddress:   endpoint,
		Logger:          nil,
		Tags:            map[string]string{},
		ProfileTypes: []pyroscope.ProfileType{
			// these profile types are enabled by default:
			pyroscope.ProfileCPU,
			pyroscope.ProfileAllocObjects,
			pyroscope.ProfileAllocSpace,
			pyroscope.ProfileInuseObjects,
			pyroscope.ProfileInuseSpace,

			// these profile types are optional:
			pyroscope.ProfileGoroutines,
			pyroscope.ProfileMutexCount,
			pyroscope.ProfileMutexDuration,
			pyroscope.ProfileBlockCount,
			pyroscope.ProfileBlockDuration,
		},
	})
}

type traceContextKey string

const spanKindKey = traceContextKey("spanKind")

func SetSpanKind(ctx context.Context, kind tr.SpanKind) context.Context {
	return context.WithValue(ctx, spanKindKey, kind)
}

func start(ctx context.Context, name string, opts ...tr.SpanStartOption) (context.Context, tr.Span) {
	parent := tr.SpanFromContext(ctx)
	tracer := parent.TracerProvider().Tracer("premai.io/Ayup/go/internal/trace")

	kind, ok := ctx.Value(spanKindKey).(tr.SpanKind)
	if ok {
		opts = append(opts, tr.WithSpanKind(kind))
	}

	return tracer.Start(ctx, name, opts...)
}

func Span(ctx context.Context, name string, attrs ...attr.KeyValue) (context.Context, tr.Span) {
	return start(ctx, name, tr.WithAttributes(attrs...))
}

func LinkedSpan(ctx context.Context, name string, linkTo tr.Span, newRoot bool, attrs ...attr.KeyValue) (context.Context, tr.Span) {
	link := tr.Link{
		SpanContext: linkTo.SpanContext(),
	}
	return start(ctx, name, tr.WithAttributes(attrs...), tr.WithNewRoot(), tr.WithLinks(link))
}

func Event(ctx context.Context, name string, attrs ...attr.KeyValue) {
	parent := tr.SpanFromContext(ctx)
	parent.AddEvent(name, tr.WithAttributes(attrs...))
}
