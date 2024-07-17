package login

import (
	"context"
	"fmt"
	"time"

	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/rpc"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
)

type Login struct {
	Host string
}

func (s *Login) Run(pctx context.Context) error {
	ctx, span := trace.Span(pctx, "login")
	defer span.End()

	c, err := rpc.Client(ctx, s.Host)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	res, err := c.Login(ctx, &pb.Credentials{Password: "bar"})
	if err != nil {
		return terror.Errorf(ctx, "grpc login: %w", err)
	}

	switch r := res.Result.(type) {
	case *pb.Authentication_Error:
		return terror.Errorf(ctx, "login error: %s", r.Error)
	case *pb.Authentication_Token:
		fmt.Println("Got authentication token", r.Token)
	}

	return nil
}
