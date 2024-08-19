package login

import (
	"context"
	"fmt"

	"github.com/libp2p/go-libp2p/core/peer"
	"premai.io/Ayup/go/internal/conf"
	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/rpc"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
	"premai.io/Ayup/go/internal/tui"
)

type Login struct {
	Host       string
	P2pPrivKey string
}

func (s *Login) Run(pctx context.Context) error {
	ctx, span := trace.Span(pctx, "login")
	defer span.End()

	privKey, err := rpc.EnsurePrivKey(ctx, s.P2pPrivKey)
	if err != nil {
		return err
	}

	c, err := rpc.Client(ctx, s.Host, privKey)
	if err != nil {
		return err
	}

	peerId, err := peer.IDFromPrivateKey(privKey)
	if err != nil {
		return terror.Errorf(ctx, "peer IDFromPrivateKey: %w", err)
	}

	fmt.Println(tui.TitleStyle.Render("Peer ID:"), peerId)
	fmt.Println(tui.TitleStyle.Render("Sending login request;"), "use the server console to confirm the request from this peer ID")

	res, err := c.Login(ctx, &pb.LoginReq{})
	if err != nil {
		return terror.Errorf(ctx, "grpc login: %w", err)
	}

	if res.GetError() != nil {
		return fmt.Errorf("remote error: %s", res.GetError().Error)
	}

	fmt.Println(tui.TitleStyle.Render("Authorized!"), "The server will now accept requests from this client.")

	if err := conf.Set(ctx, "AYUP_PUSH_HOST", s.Host); err == nil {
		fmt.Println("Setting this server as the push default, you can override it with", tui.TitleStyle.Render("AYUP_PUSH_HOST"))
	}

	return nil
}
