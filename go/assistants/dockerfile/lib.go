package dockerfile

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/moby/buildkit/client/llb/imagemetaresolver"
	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	solverPb "github.com/moby/buildkit/solver/pb"
	"go.opentelemetry.io/otel/attribute"

	"premai.io/Ayup/go/assistants/exec"
	"premai.io/Ayup/go/internal/assist"
	"premai.io/Ayup/go/internal/fs"
	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
)

type Assistant struct {
}

var _ assist.Assistant = (*Assistant)(nil)

func (s *Assistant) Name() string {
	return assist.FullName(assist.Builtin, "dockerfile")
}

func (s *Assistant) MayWork(aCtx assist.Context, _ assist.State) (bool, error) {
	aCtx, span := aCtx.Span("dockerfile MayWork")
	defer span.End()

	_, err := os.Stat(filepath.Join(aCtx.AppPath, "Dockerfile"))
	if err != nil {
		if !os.IsNotExist(err) {
			return false, terror.Errorf(aCtx.Ctx, "stat Dockerfile: %w", err)
		}

		span.AddEvent("no Dockerfile")
		return false, nil
	}

	if err := aCtx.Stream.Send(&pb.ActReply{
		Source: "ayup",
		Variant: &pb.ActReply_Log{
			Log: "Found Dockerfile, will use it",
		},
	}); err != nil {
		return false, err
	}

	return true, nil
}

func (s *Assistant) Assist(aCtx assist.Context, state assist.State) (assist.State, error) {
	aCtx, span := aCtx.Span("dockerfile")
	defer span.End()

	dfBytes, err := fs.ReadFile(aCtx.Ctx, aCtx.AppPath, "Dockerfile")
	if err != nil {
		return state, err
	}

	caps := solverPb.Caps.CapSet(solverPb.Caps.All())

	st, img, _, _, err := dockerfile2llb.Dockerfile2LLB(aCtx.Ctx, dfBytes, dockerfile2llb.ConvertOpt{
		MetaResolver: imagemetaresolver.Default(),
		LLBCaps:      &caps,
	})
	if err != nil {
		return state, terror.Errorf(aCtx.Ctx, "Dockerfile2LLB: %w", err)
	}

	dt, err := st.Marshal(aCtx.Ctx, state.GetPlatform())
	if err != nil {
		return state, terror.Errorf(aCtx.Ctx, "st Marshal: %w", err)
	}

	state, err = state.SetBuildDef(aCtx.Ctx, dt)
	if err != nil {
		return state, err
	}

	conf := img.Config
	cmd := conf.Entrypoint
	cmd = append(cmd, conf.Cmd...)

	if len(cmd) < 1 {
		return state, terror.Errorf(aCtx.Ctx, "No ENTRYPOINT or CMD in Dockerfile")
	}

	state, err = state.SetCmd(aCtx.Ctx, cmd)
	if err != nil {
		return state, err
	}

	if conf.WorkingDir == "" {
		conf.WorkingDir = "/"
	}

	state, err = state.SetWorkingDir(aCtx.Ctx, conf.WorkingDir)
	if err != nil {
		return state, err
	}

	var ports []uint32
	for k := range conf.ExposedPorts {
		trace.Event(aCtx.Ctx, "exposed port", attribute.String("port", k))

		ps, proto, hasProto := strings.Cut(k, "/")
		if !hasProto {
			proto = "tcp"
		}
		if proto == "tcp" {
			p, err := strconv.ParseUint(ps, 10, 16)
			if err != nil {
				return state, terror.Errorf(aCtx.Ctx, "parsing port number(`%s`): %w", k, err)
			}

			ports = append(ports, uint32(p))
		}
	}
	state, err = state.SetPorts(aCtx.Ctx, ports)
	if err != nil {
		return state, err
	}

	return state.SetNext(aCtx.Ctx, &exec.Assistant{})
}
