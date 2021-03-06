package tracing

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/ttys3/lgr"
	"github.com/ttys3/tracing/filter"
	"go.opentelemetry.io/contrib/propagators/b3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.opentelemetry.io/otel/trace"
)

type TpShutdownFunc func(ctx context.Context) error

type otelErrorHandler struct{}

func (e *otelErrorHandler) Handle(err error) {
	lgr.S().Error("[tracing] got error", "err", err)
}

var emptyTpShutdownFunc = func(_ context.Context) error {
	return nil
}

func applayOptions(opts ...Option) *options {
	options := &options{
		otelGrpcEndpoint: "",
		serviceName:      "no-name",
		serviceVersion:   "0.0.0",
		durationFilter:   false,
		durationMin:      time.Millisecond * 200,
		durationMax:      time.Minute,
	}
	for _, o := range opts {
		o.apply(options)
	}
	return options
}

// InitOtlpTracerProvider init a tracer provider with otlp exporter with B3 propagator
func InitOtlpTracerProvider(ctx context.Context, opts ...Option) (TpShutdownFunc, error) {
	otel.SetErrorHandler(&otelErrorHandler{})

	opt := applayOptions(opts...)

	expOptions := []otlptracegrpc.Option{
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(opt.otelGrpcEndpoint),
	}

	grpcConnectionTimeout := 3 * time.Second
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, grpcConnectionTimeout)
	defer cancel()

	traceExp, err := otlptracegrpc.New(ctx, expOptions...)
	if err != nil {
		return emptyTpShutdownFunc, fmt.Errorf("failed to create the collector trace exporter (%w)", err)
	}

	attrs := []attribute.KeyValue{
		semconv.ServiceNameKey.String(opt.serviceName),
		semconv.ServiceVersionKey.String(opt.serviceVersion),
	}
	if opt.deploymentEnvironment != "" {
		attrs = append(attrs, semconv.DeploymentEnvironmentKey.String(opt.deploymentEnvironment))
	}
	attrs = append(attrs, opt.attributes...)

	res, err := resource.New(ctx,
		resource.WithAttributes(attrs...),
	)
	if err != nil {
		return emptyTpShutdownFunc, fmt.Errorf("failed to create resource (%w)", err)
	}

	// sdktrace.WithBatcher(traceExp,
	// sdktrace.WithBatchTimeout(5*time.Second),
	// sdktrace.WithMaxExportBatchSize(10)),
	batchProcessor := sdktrace.NewBatchSpanProcessor(traceExp,
		sdktrace.WithBatchTimeout(5*time.Second),
		sdktrace.WithMaxExportBatchSize(10),
	)

	spanProcessor := batchProcessor
	if opt.durationFilter {
		// Build a SpanProcessor chain to only allow spans shorter than
		// an minute and longer than a second to be exported with the exportSP.
		spanProcessor = filter.DurationFilter{
			Next: batchProcessor,
			Min:  opt.durationMin,
			Max:  opt.durationMax,
		}
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(1))),
		sdktrace.WithSpanProcessor(spanProcessor),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	propagator := b3.New(b3.WithInjectEncoding(b3.B3MultipleHeader))
	otel.SetTextMapPropagator(propagator)

	// tracer = tp.Tracer("github.com/ttys3/tracing")

	return tp.Shutdown, nil
}

// InitStdoutTracerProvider is only for unit tests
func InitStdoutTracerProvider() (TpShutdownFunc, error) {
	exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		lgr.S().Fatal("new stdoutrace failed", "err", err)
		return emptyTpShutdownFunc, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithBatcher(exporter),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	// tracer = tp.Tracer("demo-stdouttrace")

	return tp.Shutdown, nil
}

func TracerProviderShutdown(ctx context.Context) error {
	if tp, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); ok {
		lgr.S().Info("shutdown otel tp")
		return tp.Shutdown(ctx)
	}
	return nil
}

// Start creates a span and a context.Context containing the newly-created span.
// If the context.Context provided in `ctx` contains a Span then the newly-created
// Span will be a child of that span, otherwise it will be a root span. This behavior
// can be overridden by providing `WithNewRoot()` as a SpanOption, causing the
// newly-created Span to be a root span even if `ctx` contains a Span.
func Start(ctx context.Context, spanName string, opts ...trace.SpanStartOption) (ctxWithSpan context.Context, newSpan trace.Span) {
	// nolint: forbidigo
	ctxWithSpan, newSpan = otel.Tracer("github.com/ttys3/tracing").Start(ctx, spanName, opts...)
	return
}

func TraceID(ctx context.Context) string {
	if span := trace.SpanContextFromContext(ctx); span.HasTraceID() {
		return span.TraceID().String()
	}
	return ""
}

func SpanID(ctx context.Context) string {
	if span := trace.SpanContextFromContext(ctx); span.HasSpanID() {
		return span.SpanID().String()
	}
	return ""
}

func Span(ctx context.Context) trace.Span {
	// SpanFromContext will return a `noopSpan` if ctx is not a valid span
	return trace.SpanFromContext(ctx)
}

// CtxWithSpan wrap a span with parent context
func CtxWithSpan(parent context.Context, span trace.Span) context.Context {
	return trace.ContextWithSpan(parent, span)
}

func NewSpanFromB3(ctx context.Context, header http.Header) trace.Span {
	propagator := b3.New()
	ctx = propagator.Extract(ctx, propagation.HeaderCarrier(header))
	sp := trace.SpanFromContext(ctx)
	return sp
}

func Logger(ctx context.Context, keyValues ...interface{}) lgr.Logger {
	kvs := []interface{}{
		"trace_id", TraceID(ctx),
	}
	kvs = append(kvs, keyValues...)
	return lgr.S().With(kvs...)
}
