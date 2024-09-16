package assist

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"

	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"
	"go.opentelemetry.io/otel/attribute"
	tr "go.opentelemetry.io/otel/trace"

	gateway "github.com/moby/buildkit/frontend/gateway/client"
	gatewayapi "github.com/moby/buildkit/frontend/gateway/pb"
	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
)

type RecvReq struct {
	Req *pb.ActReq
	Err error
}

type Context struct {
	Ctx         context.Context
	SendMutex   *sync.Mutex
	Stream      pb.Srv_AssistServer
	Client      *client.Client
	RecvChan    chan RecvReq
	OnLog       func([]byte)
	AppPath     string
	StatePath   string
	ScratchPath string
}

func (s *Context) Span(name string, attrs ...attribute.KeyValue) (Context, tr.Span) {
	ctx, span := trace.Span(s.Ctx, name, attrs...)

	return Context{
		Ctx:         ctx,
		SendMutex:   s.SendMutex,
		Stream:      s.Stream,
		Client:      s.Client,
		RecvChan:    s.RecvChan,
		OnLog:       s.OnLog,
		AppPath:     s.AppPath,
		StatePath:   s.StatePath,
		ScratchPath: s.ScratchPath,
	}, span
}

func (s *Context) Send(msg *pb.ActReply) error {
	s.SendMutex.Lock()
	defer s.SendMutex.Unlock()

	if err := s.Stream.Send(msg); err != nil {
		return terror.Errorf(s.Ctx, "stream Send: %w", err)
	}

	return nil
}

type logWriter struct {
	aCtx   Context
	source string
	onLog  func([]byte)
}

func byteToIntSlice(bs []byte) []int {
	ints := make([]int, len(bs))

	for i, b := range bs {
		ints[i] = int(b)
	}

	return ints
}

func (s *logWriter) Write(p []byte) (int, error) {
	// TODO: limit size?
	trace.Event(s.aCtx.Ctx, "log write", attribute.IntSlice("bytes", byteToIntSlice(p)))
	if err := s.aCtx.Send(&pb.ActReply{
		Source: s.source,
		Variant: &pb.ActReply_Log{
			Log: string(bytes.TrimRight(p, "\v")),
		},
	}); err != nil {
		return 0, err
	}
	if s.onLog != nil {
		s.onLog(p)
	}
	return len(p), nil
}

func (s *logWriter) Close() error {
	return nil
}

func (s Context) ExecProc(ctr gateway.Container, source string, cwd string, cmd []string) error {
	logWriter := logWriter{aCtx: s, source: source, onLog: s.OnLog}

	if err := s.Send(&pb.ActReply{
		Source: "ayup",
		Variant: &pb.ActReply_Log{
			Log: fmt.Sprintf("Executing `%s`", strings.Join(cmd, " ")),
		},
	}); err != nil {
		return err
	}

	pid, err := ctr.Start(s.Ctx, gateway.StartRequest{
		Cwd:    cwd,
		Args:   cmd,
		Tty:    false,
		Stdout: &logWriter,
		Stderr: &logWriter,
	})
	if err != nil {
		return terror.Errorf(s.Ctx, "ctr Start: %w", err)
	}

	waitChan := make(chan error)

	go func() {
		var retErr error

		if err := pid.Wait(); err != nil {
			var exitError *gatewayapi.ExitError
			if ok := errors.As(err, &exitError); ok {
				trace.Event(s.Ctx, "Child exited",
					attribute.Int("exitCode", int(exitError.ExitCode)),
					attribute.String("error", exitError.Error()),
				)

				if exitError.ExitCode >= gatewayapi.UnknownExitStatus {
					retErr = exitError.Err
				}
			}
		}

		if retErr != nil {
			waitChan <- terror.Errorf(s.Ctx, "pid Wait: %w", retErr)
		} else {
			waitChan <- nil
		}
	}()

	cancelCount := 0

	for {
		select {
		case err := <-waitChan:
			if err != nil {
				return err
			}
			return nil
		case req := <-s.RecvChan:
			trace.Event(s.Ctx, "Got user request")

			if req.Err != nil {
				return req.Err
			}
			if req.Req.GetCancel() {
				trace.Event(s.Ctx, "Got cancel", attribute.Int("count", cancelCount))

				switch cancelCount {
				case 0:
					if err := pid.Signal(s.Ctx, syscall.SIGINT); err != nil {
						return terror.Errorf(s.Ctx, "pid Signal: %w", err)
					}
				case 1:
					if err := pid.Signal(s.Ctx, syscall.SIGTERM); err != nil {
						return terror.Errorf(s.Ctx, "pid Signal: %w", err)
					}

				case 2:
					if err := pid.Signal(s.Ctx, syscall.SIGKILL); err != nil {
						return terror.Errorf(s.Ctx, "pid Signal: %w", err)
					}
				default:
					return terror.Errorf(s.Ctx, "more than 3 cancel attempts")
				}
				cancelCount += 1
			} else {
				return terror.Errorf(s.Ctx, "Unexpected message")
			}
		}
	}
}

func (s *Context) BuildkitStatusSender(source string, onLog func([]byte)) chan *client.SolveStatus {
	statusChan := make(chan *client.SolveStatus)
	sendLog := func(text string) {
		_ = s.Send(&pb.ActReply{
			Source: source,
			Variant: &pb.ActReply_Log{
				Log: text,
			},
		})
	}

	go func() {
		verts := make(map[digest.Digest]int)

		for msg := range statusChan {
			for _, warn := range msg.Warnings {
				sendLog(fmt.Sprintf("Warning: %v", warn))
			}
			for _, vert := range msg.Vertexes {
				vertNo, ok := verts[vert.Digest]
				if !ok {
					vertNo = len(verts) + 1
					verts[vert.Digest] = vertNo
				}

				state := "NEW"
				if vert.Started != nil {
					state = "START"
				}

				if vert.Cached {
					state = "CACHED"
				} else if vert.Completed != nil {
					state = "DONE"
				}

				duration := 0.0
				if vert.Completed != nil && vert.Started != nil {
					duration = vert.Completed.Sub(*vert.Started).Seconds()
				}

				if duration < 0.01 {
					sendLog(fmt.Sprintf("#%d %6s %s\n", vertNo, state, vert.Name))
				} else {
					sendLog(fmt.Sprintf("#%d %6s %.2fs %s\n", vertNo, state, duration, vert.Name))
				}
			}

			var prevLog *client.VertexLog
			for _, log := range msg.Logs {
				if prevLog != nil && prevLog.Vertex == log.Vertex && prevLog.Timestamp == log.Timestamp {
					continue
				}
				prevLog = log

				if onLog != nil {
					onLog(log.Data)
				}

				trace.Event(s.Ctx, "buildkit log",
					attribute.String("text", string(log.Data)),
					attribute.IntSlice("bytes", byteToIntSlice(log.Data)),
				)

				sendLog(string(log.Data))
			}

		}
	}()

	return statusChan
}
