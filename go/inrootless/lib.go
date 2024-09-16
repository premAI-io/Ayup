package inrootless

import (
	"context"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	tr "go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	"premai.io/Ayup/go/internal/conf"
	pb "premai.io/Ayup/go/internal/grpc/inrootless"
	"premai.io/Ayup/go/internal/proc"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
)

type inrSrv struct {
	pb.UnimplementedInRootlessServer
}

func (inrSrv) Ping(context.Context, *pb.PingRequest) (*pb.PingResponse, error) {
	return &pb.PingResponse{}, nil
}

func fixup(ctx context.Context) error {
	ctx, span := trace.Span(ctx, "fixup")
	defer span.End()

	// TODO: This could be better handled by setting the CNI cache path in Buildkit
	if err := os.RemoveAll("/var/lib/cni/networks"); err != nil {
		return terror.Errorf(ctx, "os RemoveAll: %w", err)
	}

	if err := os.RemoveAll("/var/lib/cni/results"); err != nil {
		return terror.Errorf(ctx, "os RemoveAll: %w", err)
	}

	return nil
}

func RunServer(ctx context.Context, builkitCmdArgs []string) error {
	ctx, span := trace.Span(ctx, "inrootless")
	ctx = trace.SetSpanKind(ctx, tr.SpanKindServer)
	defer span.End()

	if err := fixup(ctx); err != nil {
		return err
	}

	cmd := exec.Command("buildkitd", builkitCmdArgs...)

	ctx, stopSigFunc := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSigFunc()

	srv := grpc.NewServer(
		grpc.StatsHandler(
			otelgrpc.NewServerHandler(
				otelgrpc.WithTracerProvider(span.TracerProvider()),
				otelgrpc.WithMessageEvents(otelgrpc.ReceivedEvents, otelgrpc.SentEvents),
			),
		),
	)
	pb.RegisterInRootlessServer(srv, &inrSrv{})

	lis, err := net.Listen("unix", conf.InrootlessAddr())
	if err != nil {
		return terror.Errorf(ctx, "net Listen: %w", err)
	}

	ctx, buildkitSpan := trace.Span(ctx, "buildkitd")
	defer buildkitSpan.End()

	var g errgroup.Group

	_, pout := proc.Start(&g, ctx, cmd)

	g.Go(func() error {
		for pout := range pout {
			if pout.Err != nil {
				return pout.Err
			}

			if pout.Ret != nil {
				return nil
			}
		}

		stopSigFunc()
		return nil
	})

	g.Go(func() error {
		if err := srv.Serve(lis); err != nil {
			stopSigFunc()
			return terror.Errorf(ctx, "serve: %w", err)
		}
		return nil
	})

	go func() {
		<-ctx.Done()

		srv.GracefulStop()
	}()

	if err := g.Wait(); err != nil {
		return err
	}

	return nil
}
