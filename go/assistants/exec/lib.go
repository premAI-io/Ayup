package exec

import (
	"context"
	"os"

	"github.com/moby/buildkit/client"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	solverPb "github.com/moby/buildkit/solver/pb"
	"github.com/tonistiigi/fsutil"
	"go.opentelemetry.io/otel/attribute"

	"premai.io/Ayup/go/internal/assist"
	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
)

type Assistant struct {
}

var _ assist.Assistant = (*Assistant)(nil)

func (s *Assistant) Name() string {
	return assist.FullName(assist.Builtin, "exec")
}

func (s *Assistant) MayWork(aCtx assist.Context, state assist.State) (bool, error) {
	aCtx, span := aCtx.Span("exec MayWork")
	defer span.End()

	if state.GetBuildDef() == nil {
		trace.Event(aCtx.Ctx, "no builddef")

		return false, nil
	}

	return true, nil
}

func (s *Assistant) Assist(aCtx assist.Context, state assist.State) (assist.State, error) {
	aCtx, span := aCtx.Span("exec")
	defer span.End()

	ctxLogFile, err := os.OpenFile(state.Join("log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return state, terror.Errorf(aCtx.Ctx, "os OpenFile: %w", err)
	}
	defer func() {
		terror.Ackf(aCtx.Ctx, "ctxLogFile: %w", ctxLogFile.Close())
	}()

	logToFile := func(b []byte) {
		trace.Event(aCtx.Ctx, "onLog", attribute.Int("len", len(b)))
		_, err := ctxLogFile.Write(b)
		terror.Ackf(aCtx.Ctx, "ctxLogFile Write: %w", err)
	}
	aCtx.OnLog = logToFile

	b := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		def := state.GetBuildDef()

		r, err := c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, terror.Errorf(aCtx.Ctx, "client solve: %w", err)
		}

		ctr, err := c.NewContainer(ctx, gateway.NewContainerRequest{
			Mounts: []gateway.Mount{
				{
					Dest:      "/",
					MountType: solverPb.MountType_BIND,
					Ref:       r.Ref,
				},
			},
		})
		if err != nil {
			return nil, terror.Errorf(ctx, "gateway client NewContainer: %w", err)
		}
		defer func() { terror.Ackf(ctx, "ctr Release: %w", ctr.Release(ctx)) }()

		if err := aCtx.ExecProc(ctr, "app", state.GetWorkingDir(), state.GetCmd()); err != nil {
			return nil, err
		}

		return r, nil
	}

	contextFS, err := fsutil.NewFS(aCtx.AppPath)
	if err != nil {
		return state, terror.Errorf(aCtx.Ctx, "fsutil newfs: %w", err)
	}

	for _, p := range state.GetPorts() {
		err := aCtx.Send(&pb.ActReply{
			Variant: &pb.ActReply_Expose{
				Expose: &pb.ExposePort{
					Port: p,
				},
			},
		})
		if err != nil {
			return state, err
		}
	}

	if _, err := aCtx.Client.Build(aCtx.Ctx, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			"context": contextFS,
		},
	}, "ayup", b, aCtx.BuildkitStatusSender("exec", aCtx.OnLog)); err != nil {
		return state, terror.Errorf(aCtx.Ctx, "build: %w", err)
	}

	return state, nil
}
