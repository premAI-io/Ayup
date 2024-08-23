package srv

import (
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/gofiber/contrib/otelfiber"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/proxy"

	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
)

func mkProxy() *fiber.App {
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		BodyLimit:             1024 * 1024 * 1024,
	})
	app.Use(otelfiber.Middleware())

	app.Use(func(c *fiber.Ctx) error {
		if strings.HasPrefix(c.Hostname(), "app.") {
			return proxy.Do(c, "http://localhost:5000"+c.OriginalURL())
		}

		return fiber.NewError(fiber.StatusNotFound, "Not found!")
	})

	return app
}

func (s *Srv) Forward(stream pb.Srv_ForwardServer) error {
	genericError := fmt.Errorf("port forwarding failure")
	ctx := stream.Context()
	conn, err := net.Dial("tcp", "127.0.0.1:5000")
	if err != nil {
		terror.Ackf(ctx, "net dial: %w", err)
		return genericError
	}
	defer func() { terror.Ackf(ctx, "conn close: %w", conn.Close()) }()
	trace.Event(ctx, "connected to port 5000")

	doneChan := make(chan error)

	ingress := func() {
		for {
			req, err := stream.Recv()
			if err != nil {
				if err != io.EOF {
					terror.Ackf(ctx, "stream recv: %w", err)
					doneChan <- genericError
				} else {
					trace.Event(ctx, "ingress done")
					doneChan <- nil
				}
				break
			}

			trace.Event(ctx, "ingress recv")

			if _, err := conn.Write(req.Data); err != nil {
				terror.Ackf(ctx, "conn write: %w", err)
				doneChan <- genericError
				return
			}

			trace.Event(ctx, "ingress write")
		}

		terror.Ackf(ctx, "conn close: %w", conn.Close())
	}

	egress := func() {
		buf := make([]byte, 16*1024)
		for {
			len, err := conn.Read(buf)
			if err != nil {
				if err != io.EOF {
					terror.Ackf(ctx, "conn read: %w", err)
					doneChan <- genericError
					break
				}

				if err := stream.Send(&pb.ForwardResponse{
					Closed: true,
				}); err != nil {
					terror.Ackf(ctx, "stream send: %w", err)
				}

				trace.Event(ctx, "egress done")
				doneChan <- nil
				break
			}

			trace.Event(ctx, "egress read")

			if err := stream.Send(&pb.ForwardResponse{
				Data: buf[:len],
			}); err != nil {
				terror.Ackf(ctx, "stream send: %w", err)
				doneChan <- genericError
				break
			}

			trace.Event(ctx, "egress send")
		}
	}

	go ingress()
	go egress()

	doneCount := 0
	for chanErr := range doneChan {
		if err == nil {
			err = chanErr
		}

		doneCount += 1
		if doneCount > 1 {
			break
		}
	}

	return err
}
