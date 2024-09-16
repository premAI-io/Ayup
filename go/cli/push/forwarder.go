package push

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"

	pb "premai.io/Ayup/go/internal/grpc/srv"
)

type Forwarder struct {
	wg     sync.WaitGroup
	Client pb.SrvClient

	listeners map[uint32]net.Listener
}

func newForwarder(client pb.SrvClient) Forwarder {
	return Forwarder{
		Client:    client,
		listeners: map[uint32]net.Listener{},
	}
}

func (s *Forwarder) startPortForwarder(ctx context.Context, port uint32) error {
	ctx, span := trace.Span(ctx, "start port forwarder")
	defer span.End()

	listener, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		return terror.Errorf(ctx, "net listen: %w", err)
	}

	s.listeners[port] = listener
	trace.Event(ctx, "TCP proxy listening", attribute.Int("port", int(port)))

	egress := func(ctx context.Context, wg *sync.WaitGroup, conn net.Conn, stream pb.Srv_ForwardClient) {
		defer wg.Done()
		ctx, span := trace.Span(ctx, "egress")
		defer span.End()

		buf := make([]byte, 16*1024)
		for {
			len, err := conn.Read(buf)
			if err != nil {
				if err != io.EOF {
					terror.Ackf(ctx, "conn read: %w", err)
				}

				if err := stream.CloseSend(); err != nil {
					terror.Ackf(ctx, "stream close send: %w", err)
				}
				return
			}

			trace.Event(ctx, "egress conn read")

			if err := stream.Send(&pb.ForwardRequest{
				Data: buf[:len],
				Port: port,
			}); err != nil {
				terror.Ackf(ctx, "stream send: %w", err)
				return
			}

			trace.Event(ctx, "egress stream send")
		}
	}

	ingress := func(ctx context.Context, wg *sync.WaitGroup, conn net.Conn, stream pb.Srv_ForwardClient) {
		defer wg.Done()
		ctx, span := trace.Span(ctx, "ingress")
		defer span.End()

		for {
			res, err := stream.Recv()
			if err != nil {
				if err != io.EOF {
					terror.Ackf(ctx, "stream recv: %w", err)
				}
				break
			}

			if res.Closed {
				break
			}

			trace.Event(ctx, "ingress stream recv")

			if _, err := conn.Write(res.Data); err != nil {
				if !errors.Is(err, net.ErrClosed) {
					terror.Ackf(ctx, "conn write: %w", err)
				}
				return
			}

			trace.Event(ctx, "ingress conn write")
		}

		terror.Ackf(ctx, "conn close: %w", conn.Close())
	}

	handler := func(ctx context.Context, conn net.Conn) {
		defer func() { terror.Ackf(ctx, "proxy conn close: %w", conn.Close()) }()
		ctx, span := trace.Span(ctx, "handler")
		defer span.End()

		stream, err := s.Client.Forward(ctx)
		if err != nil {
			terror.Ackf(ctx, "client forward: %w", err)
			return
		}

		var wg sync.WaitGroup
		wg.Add(2)

		go ingress(ctx, &wg, conn, stream)
		go egress(ctx, &wg, conn, stream)

		wg.Wait()
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, span := trace.LinkedSpan(ctx, "forwarder listen", span, true)
		defer span.End()

		conns := make([]net.Conn, 0, 16)
		for {
			conn, err := listener.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					terror.Ackf(ctx, "listener accept: %w", err)
				} else {
					trace.Event(ctx, "listener done")
				}
				break
			}

			trace.Event(ctx, "port forwarder listener accept")

			conns = append(conns, conn)
			go handler(ctx, conn)
		}

		for _, conn := range conns {
			_ = conn.Close()
		}
	}()

	return nil
}
