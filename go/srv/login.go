package srv

import (
	"context"
	"fmt"

	"github.com/charmbracelet/huh"
	"go.opentelemetry.io/otel/trace"

	"premai.io/Ayup/go/internal/conf"
	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/tui"
)

func (s *Srv) Login(ctx context.Context, in *pb.LoginReq) (*pb.LoginReply, error) {
	span := trace.SpanFromContext(ctx)

	internalError := func(err error) (*pb.LoginReply, error) {
		return &pb.LoginReply{
			Error: &pb.Error{
				Error: fmt.Sprintf("Internal Error: Support ID: %s", span.SpanContext().SpanID()),
			},
		}, err
	}

	hasAuth, err := s.checkPeerAuth(ctx)
	if err != nil {
		return internalError(terror.Errorf(ctx, "checkPeerAuth: %w", err))
	}

	if hasAuth {
		return &pb.LoginReply{}, nil
	}

	peerId, err := remotePeerId(ctx)
	if err != nil {
		return internalError(err)
	}

	if !s.tuiMutex.TryLock() {
		return &pb.LoginReply{
			Error: &pb.Error{
				Error: "Server busy",
			},
		}, nil
	}
	defer s.tuiMutex.Unlock()

	// TODO: timeout

	authed := false
	err = huh.NewConfirm().
		Title("Authorize client?").
		Description(fmt.Sprintf("Peer ID: %s", peerId.String())).
		Value(&authed).
		Run()

	if err != nil {
		return internalError(err)
	}

	if !authed {
		fmt.Println(tui.TitleStyle.Render("Authorization rejected:"), peerId.String())
		return &pb.LoginReply{
			Error: &pb.Error{
				Error: "Authorization rejected",
			},
		}, nil
	}

	fmt.Println(tui.TitleStyle.Render("Authorized client:"), peerId.String())
	s.P2pAuthedClients = append(s.P2pAuthedClients, peerId)

	_ = conf.Append(ctx, "AYUP_P2P_AUTHORIZED_CLIENTS", peerId.String())

	return &pb.LoginReply{}, nil
}
