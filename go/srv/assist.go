package srv

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	tr "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"

	"premai.io/Ayup/go/assistants/dockerfile"
	"premai.io/Ayup/go/assistants/python"
	"premai.io/Ayup/go/internal/assist"
	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/trace"

	"github.com/moby/buildkit/client"

	"premai.io/Ayup/go/internal/terror"
)

type ActServer interface {
	Send(*pb.ActReply) error
	Recv() (*pb.ActReq, error)
	grpc.ServerStream
}

type aCtx struct {
	ctx       context.Context
	sendMutex *sync.Mutex
	stream    pb.Srv_AssistServer
}

func (s *aCtx) send(msg *pb.ActReply) error {
	s.sendMutex.Lock()
	defer s.sendMutex.Unlock()

	if err := s.stream.Send(msg); err != nil {
		return terror.Errorf(s.ctx, "stream Send: %w", err)
	}

	return nil
}

func (s *aCtx) sendError(fmt string, args ...any) error {
	oerr := terror.Errorf(s.ctx, fmt, args...)
	return s.send(newErrorReply(oerr.Error()))
}

func (s *aCtx) internalError(fmt string, args ...any) error {
	_ = terror.Errorf(s.ctx, fmt, args...)
	span := tr.SpanFromContext(s.ctx)
	return s.sendError("Internal Error: Support ID: %s", span.SpanContext().SpanID())
}

var ErrUserCancelled = errors.New("user cancelled")

func (s *Srv) findWorkableAssistant(aCtx assist.Context, state assist.State) (assist.State, error) {
	aCtx, span := aCtx.Span("find workable assistant")
	defer span.End()

	dfAssist := dockerfile.Assistant{}
	dockerfileMayWork, err := dfAssist.MayWork(aCtx, state)
	if err != nil {
		return state, err
	}
	if dockerfileMayWork {
		return state.SetNext(aCtx.Ctx, &dfAssist)
	}

	pyAssist := python.Assistant{}
	pythonMayWork, err := pyAssist.MayWork(aCtx, state)
	if err != nil {
		return state, err
	}
	if pythonMayWork {
		return state.SetNext(aCtx.Ctx, &pyAssist)
	}

	return state, terror.Errorf(aCtx.Ctx, "could not find assistant that may work")
}

func (s *Srv) Assist(stream pb.Srv_AssistServer) error {
	ctx := stream.Context()
	ctx = trace.SetSpanKind(ctx, tr.SpanKindServer)

	actx := aCtx{
		ctx:       ctx,
		sendMutex: &sync.Mutex{},
		stream:    stream,
	}

	recvChan := make(chan assist.RecvReq)

	if ok, err := s.checkPeerAuth(ctx); !ok || err != nil {
		if err != nil {
			return actx.internalError("checkPeerAuth: %w", err)
		}

		return actx.sendError("Not authorized")
	}

	proxy := mkProxy()
	go func() {
		if err := proxy.Listen(":8080"); err != nil {
			terror.Ackf(ctx, "proxy listen: %w", err)
		}
	}()
	defer func() {
		if err := proxy.ShutdownWithContext(ctx); err != nil {
			terror.Ackf(ctx, "proxy shutdown: %w", err)
		}
	}()

	go func(ctx context.Context) {
		for {
			req, err := stream.Recv()
			if err != nil && err != io.EOF {
				err = terror.Errorf(ctx, "stream recv: %w", err)
			}

			recvChan <- assist.RecvReq{Req: req, Err: err}

			if err != nil {
				break
			}
		}
	}(ctx)

	c, err := client.New(ctx, s.BuildkitdAddr)
	if err != nil {
		return actx.internalError("client new: %w", err)
	}

	// TODO: Check if we are dealing with an existing session etc.
	r, ok := <-recvChan
	if !ok {
		return actx.internalError("stream recv: channel closed")
	}
	if r.Err != nil {
		return actx.internalError("stream recv: %w", r.Err)
	}

	if r.Req.Cancel {
		return actx.sendError("analysis canceled")
	}

	if r.Req.Choice != nil {
		return actx.sendError("premature choice")
	}

	state := assist.NewState(filepath.Join(s.AppDir, ".ayup"), s.StateDir, s.registry)

	if err := os.RemoveAll(state.Path); err != nil && !os.IsNotExist(err) {
		return terror.Errorf(ctx, "os RemoveAll(%s): %w", state.Path, err)
	}

	if err := os.MkdirAll(state.SrcPath, 0700); err != nil {
		return terror.Errorf(ctx, "os MkdirAll: %w", err)
	}

	if err := os.Rename(state.SrcPath, state.Path); err != nil && !os.IsNotExist(err) {
		return terror.Errorf(ctx, "os Rename(%s, %s): %w", state.SrcPath, state.Path, err)
	}

	_, state, err = state.Version(ctx)
	if err != nil {
		return actx.sendError("dot Version: %w", err)
	}

	aCtx := assist.Context{
		Ctx:         ctx,
		SendMutex:   actx.sendMutex,
		Stream:      stream,
		Client:      c,
		RecvChan:    recvChan,
		OnLog:       nil,
		AppPath:     s.AppDir,
		StatePath:   s.StateDir,
		ScratchPath: s.ScratchDir,
	}

	if s.push.hasAssistant {
		nameBs, err := assist.LoadName(ctx, s.AssistantDir)
		if err != nil {
			return err
		}
		s.registry.Del(assist.FullName(assist.Local, string(nameBs)))

		_, err = s.registry.RegisterDir(actx.ctx, assist.Local, s.AssistantDir)
		if err != nil {
			return err
		}
	}

	state, err = state.LoadState(ctx)
	if err != nil {
		return err
	}

	if state.GetNext() == nil {
		state, err = s.findWorkableAssistant(aCtx, state)
		if err != nil {
			return err
		}
	}

	for {
		assist := state.GetNext()
		if assist == nil {
			trace.Event(ctx, "next assistant not set")
			break
		}
		trace.Event(ctx, "next assistant", attribute.String("name", assist.Name()))

		mayWork, err := assist.MayWork(aCtx, state)
		if err != nil {
			return err
		}
		if !mayWork {
			return terror.Errorf(aCtx.Ctx, "The specified next assistant says it won't work")
		}

		state, err = state.SetNext(aCtx.Ctx, nil)
		if err != nil {
			return err
		}
		state, err = assist.Assist(aCtx, state)
		if err != nil {
			return err
		}
	}

	if err := os.Rename(state.Path, state.SrcPath); err != nil {
		return terror.Errorf(ctx, "os Rename(%s, %s): %w", state.Path, state.SrcPath, err)
	}

	return aCtx.Send(&pb.ActReply{})
}
