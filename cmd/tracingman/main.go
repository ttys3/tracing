package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/ttys3/lgr"
	"github.com/ttys3/tracing"
	"go.opentelemetry.io/otel/trace"
)

// see go.opentelemetry.io/contrib/propagators/b3@v1.4.0/b3_data_test.go
// go.opentelemetry.io/contrib/propagators/b3@v1.4.0/b3_integration_test.go
const (
	spanIDStr  = "00f067aa0ba902b7"
)

const (
	b3Context      = "b3"
	b3Flags        = "x-b3-flags"
	b3TraceID      = "x-b3-traceid"
	b3SpanID       = "x-b3-spanid"
	b3Sampled      = "x-b3-sampled"
	b3ParentSpanID = "x-b3-parentspanid"
)

var (
	otelGrpcEndpoint string
	serviceName      string
	rootSpanName     string
	withB3           bool
)

func main() {
	flag.StringVar(&otelGrpcEndpoint, "e", "otel-collector.service.dc1.consul:4317", "opentelemetry collector grpc endpoint")
	flag.StringVar(&rootSpanName, "n", "ThisIsMyRootSpanName", "root span name")
	flag.StringVar(&serviceName, "s", "MyDemoService", "server name")
	flag.BoolVar(&withB3, "b", false, "with b3 propagator")

	flag.Parse()

	lgr.S().Info("begin init tracer provider", "otel_grpc_endpoint", otelGrpcEndpoint, "service_name", serviceName, "root_span_name", rootSpanName)

	_, shutdownFunc, err := tracing.InitOtlpTracerProvider(context.Background(), 
	tracing.WithOtelGrpcEndpoint(otelGrpcEndpoint), 
	tracing.WithSerivceName(serviceName))
	defer shutdownFunc(context.Background())

	if err != nil {
		panic(err)
	}

	var sp trace.Span
	ctx := context.Background()
	if withB3 {
		lgr.S().Info("b3 propagator enabled")
		u := uuid.Must(uuid.NewV4())
		traceID := strings.ReplaceAll(u.String(), "-", "")
		// fmt.Printf("B3 traceID=%v\n", traceID)

		header := make(http.Header)
		for _, v := range []struct {
			Key string
			Val string
		}{
			{b3TraceID, traceID},
			{b3SpanID, spanIDStr},
			{b3Sampled, "true"},
		} {
			header.Set(v.Key, v.Val)
		}
		sp = tracing.NewSpanFromB3(ctx, header)
		if !sp.IsRecording() {
			lgr.S().Warn("parent is not recording span, create new")
			ctx, sp = tracing.SpanStart(ctx, rootSpanName)
		}
	} else {
		ctx, sp = tracing.SpanStart(ctx, rootSpanName)
	}
	defer sp.End()

	fmt.Printf("traceID:\n%v\n", tracing.TraceID(ctx))

	createTestSpan(ctx)
	fmt.Println("done")
}

func createTestSpan(ctx context.Context) {
	ctx, span := tracing.Start(ctx, "test.MySpanName")
	defer span.End()

	func() {
		ctx, span := tracing.Start(ctx, "test.MySubWork01")
		defer span.End()
		time.Sleep(time.Millisecond * 480)

		func() {
			_, span := tracing.Start(ctx, "test.MySubSubWork02")
			defer span.End()
			time.Sleep(time.Millisecond * 120)
		}()
	}()
}
