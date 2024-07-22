package srv

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/moby/buildkit/client/llb"

	tr "go.opentelemetry.io/otel/trace"

	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
)

func (s *Srv) Build(stream pb.Srv_BuildServer) (err error) {
	ctx := stream.Context()
	ctx = trace.SetSpanKind(ctx, tr.SpanKindServer)

	recvChan := mkRecvChan(ctx, stream)

	var buf bytes.Buffer

	if err = func() error {
		ctx, span := trace.Span(ctx, "mkLlb")
		defer span.End()

		dt, err := s.MkLlb(ctx)
		if err != nil {
			return err
		}

		if err := llb.WriteTo(dt, &buf); err != nil {
			return terror.Errorf(ctx, "llb writeto: %w", err)
		}

		return nil
	}(); err != nil {
		return
	}

	if err = func() (err error) {
		ctx, span := trace.Span(ctx, "buildctl")
		defer span.End()

		procWait := mkProcWaiter(ctx, stream, recvChan)

		cmd := exec.Command(
			"buildctl",
			"--addr", s.BuildkitdAddr,
			"build",
			"--output", fmt.Sprintf("type=image,name=%s", s.ImgName),
			"--local", fmt.Sprintf("context=%s", s.SrcDir),
		)

		// https://github.com/moby/moby/issues/46129#issuecomment-2016552967
		var env []string
		for _, kv := range cmd.Environ() {
			if !strings.HasPrefix(kv, "OTEL_EXPORTER_OTLP_ENDPOINT=") {
				env = append(env, kv)
			}
		}
		cmd.Env = env

		in, out := startProc(ctx, cmd)

		in <- procIn{
			stdio: buf.Bytes(),
		}

		return procWait("buildctl", in, out)
	}(); err != nil {
		return
	}

	return nil
}

func (s *Srv) MkLlb(ctx context.Context) (*llb.Definition, error) {
	local := llb.Local("context", llb.ExcludePatterns([]string{".venv", ".git"}))
	st := llb.Image("docker.io/library/python:3.12-slim").
		AddEnv("PYTHONUNBUFFERED", "True").
		File(llb.Mkdir("/app", 0755)).
		Dir("/app").
		File(llb.Copy(local, "requirements.txt", ".")).
		Run(llb.Shlex("pip install --no-cache-dir -r requirements.txt")).Root().
		File(llb.Copy(local, ".", "."))

	dt, err := st.Marshal(ctx, llb.LinuxAmd64)
	if err != nil {
		return nil, terror.Errorf(ctx, "marshal: %w", err)
	}

	return dt, nil
}
