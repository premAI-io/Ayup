package inrootless

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

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

func startConn(g *errgroup.Group, pctx context.Context, port uint32, ip string, stream pb.InRootless_ForwardServer) (net.Conn, error) {
	addr := fmt.Sprintf("%s:%d", ip, port)
	ctx, span := trace.Span(pctx, "start conn", attribute.String("addr", addr))
	defer span.End()

	fmt.Println("starting conn", addr)

	dialer := net.Dialer{
		Timeout: time.Second,
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		if err := stream.Send(&pb.ForwardResponse{
			Closed: true,
			Port:   port,
		}); err != nil {
			terror.Ackf(ctx, "stream Send: %w", err)
		}

		return nil, terror.Errorf(ctx, "net dial: %w", err)
	}

	fmt.Println("connected")

	trace.Event(ctx, "new conn")

	g.Go(func() error {
		ctx, span := trace.LinkedSpan(pctx, "conn read", span, false)
		defer span.End()

		fmt.Println("starting conn read")

		buf := make([]byte, 16*1024)
		for {
			len, err := conn.Read(buf)
			if err != nil {
				if err != io.EOF {
					terror.Ackf(ctx, "conn read: %w", err)
				}

				if err := stream.Send(&pb.ForwardResponse{
					Closed: true,
					Port:   port,
				}); err != nil {
					return terror.Errorf(ctx, "stream Send: %w", err)
				}

				trace.Event(ctx, "egress done")
				break
			}

			if err := stream.Send(&pb.ForwardResponse{
				Data: buf[:len],
				Port: port,
			}); err != nil {
				return terror.Errorf(ctx, "stream Send: %w", err)
			}
			trace.Event(ctx, "egress read")
		}

		return nil
	})

	return conn, nil
}

func (s *inrSrv) Forward(stream pb.InRootless_ForwardServer) error {
	ctx := stream.Context()

	bs, err := os.ReadFile("/var/lib/cni/networks/buildkit/last_reserved_ip.0")
	if err != nil {
		return terror.Errorf(ctx, "os ReadFile: %w", err)
	}
	ip := string(bytes.TrimSpace(bs))

	fmt.Println("starting forward")
	trace.Event(ctx, "last reserved IP", attribute.String("ip", string(bs)))

	var conn net.Conn
	var port uint32
	var g errgroup.Group

	g.Go(func() error {
		defer func() {
			if conn != nil {
				terror.Ackf(ctx, "conn Close: %w", conn.Close())
			}
		}()
		ctx, span := trace.Span(ctx, "ingress")
		defer span.End()

		for {
			req, err := stream.Recv()
			if err != nil {
				if err != io.EOF {
					return terror.Errorf(ctx, "stream recv: %w", err)
				}

				trace.Event(ctx, "ingress done")
				break
			}

			trace.Event(ctx, "ingress recv")

			if conn == nil {
				port = req.Port
				err = withDetachedNetNSIfAny(ctx, func(ctx context.Context) error {
					conn, err = startConn(&g, ctx, port, ip, stream)
					return err
				})
				if err != nil {
					return err
				}
			} else if port != req.Port {
				return terror.Errorf(ctx, "Stream started with port %d, but received message with port %d", port, req.Port)
			}

			if _, err := conn.Write(req.Data); err != nil {
				terror.Ackf(ctx, "conn write: %w", err)
			}

			trace.Event(ctx, "ingress write")
		}

		return nil
	})

	return g.Wait()
}
