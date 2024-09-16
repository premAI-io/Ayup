package assistants

import (
	"context"
	"fmt"
	"strings"

	"premai.io/Ayup/go/cli/push"
	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/rpc"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
	"premai.io/Ayup/go/internal/tui"
)

func List(pctx context.Context, host string, privKey string) error {
	ctx, span := trace.Span(pctx, "assistants list")
	defer span.End()

	c, err := rpc.ClientEnsureKey(ctx, host, privKey)
	if err != nil {
		return err
	}

	resp, err := c.AssistantsList(ctx, &pb.AssistantsListReq{})
	if err != nil {
		return terror.Errorf(ctx, "client AssistantsList: %w", err)
	}

	fmt.Println(tui.TitleStyle.Render("Assistants:"))

	for _, assist := range resp.Assistants {
		before, after, ok := strings.Cut(assist.Name, ":")
		if !ok {
			fmt.Println("\t", tui.ErrorStyle.Render(assist.Name))
		} else {
			fmt.Println("\t", tui.VersionStyle.Render(before), tui.TitleStyle.Render(after))
		}
	}

	return nil
}

func Push(ctx context.Context, host string, privKey string, path string) error {
	ctx, span := trace.Span(ctx, "assistants push")
	defer span.End()

	c, err := rpc.ClientEnsureKey(ctx, host, privKey)
	if err != nil {
		return err
	}

	pusher := push.Pusher{
		Host:         host,
		Client:       c,
		AssistantDir: path,
	}

	if err := pusher.Upload(ctx); err != nil {
		return err
	}

	if _, err := c.AssistantsPush(ctx, &pb.AssistantsPushReq{}); err != nil {
		return terror.Errorf(ctx, "client AssistantsPush: %w", err)
	}

	return nil
}
