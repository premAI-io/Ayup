package inrootless

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"

	"github.com/containernetworking/plugins/pkg/ns"
	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/sync/errgroup"
	pb "premai.io/Ayup/go/internal/grpc/inrootless"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
)

// Copied from buildkit
func withDetachedNetNSIfAny(ctx context.Context, fn func(context.Context) error) error {
	if stateDir := os.Getenv("ROOTLESSKIT_STATE_DIR"); stateDir != "" {
		detachedNetNS := filepath.Join(stateDir, "netns")
		if _, err := os.Lstat(detachedNetNS); !errors.Is(err, os.ErrNotExist) {
			return ns.WithNetNSPath(detachedNetNS, func(_ ns.NetNS) error {
				err2 := fn(ctx)
				return err2
			})
		}
	}

	return terror.Errorf(ctx, "ROOTLESSKIT_STATE_DIR not set")
}

func (s *inrSrv) Forward(stream pb.InRootless_ForwardServer) error {
	ctx := stream.Context()

	bs, err := os.ReadFile("/var/lib/cni/networks/buildkit/last_reserved_ip.0")
	if err != nil {
		return terror.Errorf(ctx, "os ReadFile: %w", err)
	}

	trace.Event(ctx, "last reserved IP", attribute.String("ip", string(bs)))

	return withDetachedNetNSIfAny(ctx, func(ctx context.Context) error {
		conn, err := net.Dial("tcp", string(bytes.TrimSpace(bs))+":5000")
		if err != nil {
			return terror.Errorf(ctx, "net dial: %w", err)
		}
		defer func() { terror.Ackf(ctx, "conn close: %w", conn.Close()) }()
		trace.Event(ctx, "connected to port 5000")

		doneChan := make(chan error)

		var g errgroup.Group

		g.Go(func() error {
			for {
				req, err := stream.Recv()
				if err != nil {
					if err != io.EOF {
						return terror.Errorf(ctx, "stream recv: %w", err)
					} else {
						trace.Event(ctx, "ingress done")
						doneChan <- nil
					}
					break
				}

				trace.Event(ctx, "ingress recv")

				if _, err := conn.Write(req.Data); err != nil {
					return terror.Errorf(ctx, "conn write: %w", err)
				}

				trace.Event(ctx, "ingress write")
			}

			return terror.Errorf(ctx, "conn close: %w", conn.Close())
		})

		g.Go(func() error {
			buf := make([]byte, 16*1024)
			for {
				len, err := conn.Read(buf)
				if err != nil {
					if err != io.EOF {
						return terror.Errorf(ctx, "conn read: %w", err)
					}

					if err := stream.Send(&pb.ForwardResponse{
						Closed: true,
					}); err != nil {
						return terror.Errorf(ctx, "stream send: %w", err)
					}

					trace.Event(ctx, "egress done")
					return nil
				}

				trace.Event(ctx, "egress read")

				if err := stream.Send(&pb.ForwardResponse{
					Data: buf[:len],
				}); err != nil {
					return terror.Errorf(ctx, "stream send: %w", err)
				}

				trace.Event(ctx, "egress send")
			}
		})

		return g.Wait()
	})
}
