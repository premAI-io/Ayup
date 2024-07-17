package rpc

import (
	"context"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/terror"
)

func Client(ctx context.Context, host string) (pb.SrvClient, error) {
	provider := trace.SpanFromContext(ctx).TracerProvider()
	conn, err := grpc.NewClient(host,
		grpc.WithStatsHandler(
			otelgrpc.NewClientHandler(
				otelgrpc.WithTracerProvider(provider),
				otelgrpc.WithMessageEvents(otelgrpc.ReceivedEvents, otelgrpc.SentEvents),
			),
		),
		grpc.WithTransportCredentials(insecure.NewCredentials()))

	if err != nil {
		return nil, terror.Errorf(ctx, "grpc dial: %w", err)
	}

	return pb.NewSrvClient(conn), nil
}
