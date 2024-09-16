package srv

import (
	"fmt"
	"io"
	"strings"

	"github.com/gofiber/contrib/otelfiber"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/proxy"
	"golang.org/x/sync/errgroup"

	inrPb "premai.io/Ayup/go/internal/grpc/inrootless"
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
	ctx := stream.Context()
	genericError := fmt.Errorf("port forwarding failure")

	inrStream, err := s.inrClient.Forward(ctx)
	if err != nil {
		terror.Ackf(ctx, "inrClient Forward: %w", err)
		return genericError
	}

	var g errgroup.Group

	g.Go(func() error {
		for {
			req, err := stream.Recv()
			if err != nil {
				if err != io.EOF {
					terror.Ackf(ctx, "stream recv: %w", err)
					return genericError
				} else {
					trace.Event(ctx, "ingress done")
					return nil
				}
			}

			if err := inrStream.Send(&inrPb.ForwardRequest{
				Data: req.Data,
				Port: req.Port,
			}); err != nil {
				terror.Ackf(ctx, "inrStream Send: %w", err)
				return genericError
			}

			trace.Event(ctx, "ingress recv")
		}
	})

	g.Go(func() error {
		for {
			req, err := inrStream.Recv()
			if err != nil {
				if err != io.EOF {
					terror.Ackf(ctx, "stream recv: %w", err)
					return genericError
				} else {
					trace.Event(ctx, "ingress done")
					return nil
				}
			}

			if req.Closed {
				if err := stream.Send(&pb.ForwardResponse{
					Closed: true,
					Port:   req.Port,
				}); err != nil {
					terror.Ackf(ctx, "stream send: %w", err)
					return genericError
				}
				break
			}

			if err := stream.Send(&pb.ForwardResponse{
				Data: req.Data,
				Port: req.Port,
			}); err != nil {
				terror.Ackf(ctx, "inrStream Send: %w", err)
				return genericError
			}

			trace.Event(ctx, "ingress recv")
		}

		return nil
	})

	return g.Wait()
}
