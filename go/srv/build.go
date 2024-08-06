package srv

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/opencontainers/go-digest"

	// gatewayapi "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/tonistiigi/fsutil"

	tr "go.opentelemetry.io/otel/trace"

	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
)

func filterEnv(cmd *exec.Cmd) []string {
	// https://github.com/moby/moby/issues/46129#issuecomment-2016552967
	var env []string
	for _, kv := range cmd.Environ() {
		if !strings.HasPrefix(kv, "OTEL_EXPORTER_OTLP_ENDPOINT=") {
			env = append(env, kv)
		}
	}

	return env
}

func (s *Srv) Build(stream pb.Srv_BuildServer) (err error) {
	ctx := stream.Context()
	ctx = trace.SetSpanKind(ctx, tr.SpanKindServer)

	recvChan := mkRecvChan(ctx, stream)

	if s.push.analysis.UseDockerfile {
		ctx, span := trace.Span(ctx, "buildctl")
		defer span.End()

		procWait := mkProcWaiter(ctx, stream, recvChan)

		cmd := exec.Command(
			"buildctl",
			"--addr", s.BuildkitdAddr,
			"build",
			"--frontend=dockerfile.v0",
			"--output", fmt.Sprintf("type=image,name=%s", s.ImgName),
			"--local", fmt.Sprintf("context=%s", s.SrcDir),
			"--local", fmt.Sprintf("dockerfile=%s", s.SrcDir),
		)
		cmd.Env = filterEnv(cmd)

		in, out := startProc(ctx, cmd)

		return procWait("buildctl", in, out)
	}

	if err = func() (err error) {
		ctx, span := trace.Span(ctx, "buildctl")
		defer span.End()

		internalError := mkInternalError(ctx, stream)

		c, err := client.New(ctx, s.BuildkitdAddr)
		if err != nil {
			return internalError("client new: %w", err)
		}

		b := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			def, err := s.MkLlb(ctx)
			if err != nil {
				return nil, internalError("mkllb: %w", err)
			}

			r, err := c.Solve(ctx, gateway.SolveRequest{
				Definition: def.ToPB(),
			})
			if err != nil {
				return nil, internalError("client solve: %w", err)
			}

			return r, nil
		}

		statusChan := make(chan *client.SolveStatus)
		sendLog := func(source string, text string) {
			terror.Ackf(ctx, "send log stream send: %w", stream.Send(&pb.ActReply{
				Source: source,
				Variant: &pb.ActReply_Log{
					Log: text,
				},
			}))
		}

		go func() {
			verts := make(map[digest.Digest]int)

			for msg := range statusChan {
				for _, log := range msg.Logs {
					sendLog("buildkit", fmt.Sprintf("Log: #%d %v %s", log.Stream, log.Timestamp, string(log.Data)))
				}
				for _, warn := range msg.Warnings {
					sendLog("buildkit", fmt.Sprintf("Warning: %v", warn))
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
						sendLog("buildkit", fmt.Sprintf("#%d %6s %s", vertNo, state, vert.Name))
					} else {
						sendLog("buildkit", fmt.Sprintf("#%d %6s %.2fs %s", vertNo, state, duration, vert.Name))
					}
				}
			}
		}()

		contextFS, err := fsutil.NewFS(s.SrcDir)
		if err != nil {
			return internalError("fsutil newfs: %w", err)
		}

		_, err = c.Build(ctx, client.SolveOpt{
			Exports: []client.ExportEntry{
				{
					Type: client.ExporterImage,
					Attrs: map[string]string{
						"name":        s.ImgName,
						"push":        "false",
						"unpack":      "false",
						"compression": "uncompressed",
					},
				},
			},
			LocalMounts: map[string]fsutil.FS{
				"context": contextFS,
			},
		}, "ayup", b, statusChan)

		if err != nil {
			return internalError("client build: %w", err)
		}

		return nil
	}(); err != nil {
		return
	}

	if err := stream.Send(&pb.ActReply{}); err != nil {
		return terror.Errorf(ctx, "stream send: %w", err)
	}

	return nil
}

func (s *Srv) MkLlb(ctx context.Context) (*llb.Definition, error) {
	local := llb.Local("context", llb.ExcludePatterns([]string{".venv", ".git"}))
	st := llb.Image("docker.io/library/python:3.12-slim").
		AddEnv("PYTHONUNBUFFERED", "True").
		File(llb.Mkdir("/app", 0755)).
		Dir("/app").
		File(llb.Rm("/etc/apt/apt.conf.d/docker-clean"))

	aptDeps := []string{}
	if s.push.analysis.NeedsGit {
		aptDeps = append(aptDeps, "git")
	}

	if s.push.analysis.NeedsLibGL {
		aptDeps = append(aptDeps, "libgl1")
	}

	if s.push.analysis.NeedsLibGlib {
		aptDeps = append(aptDeps, "libglib2.0-0")
	}

	if len(aptDeps) > 0 {
		aptCachePath := "/var/cache/apt"

		cacheAptMnt := llb.AddMount(
			aptCachePath,
			llb.Scratch(),
			llb.AsPersistentCacheDir(aptCachePath, llb.CacheMountLocked),
		)

		st = st.Run(
			llb.Shlexf(`dash -c "apt update && apt install -y %s"`, strings.Join(aptDeps, " ")),
			cacheAptMnt,
		).Root()
	}

	st = st.File(llb.Copy(local, "requirements.txt", "."))

	pipCachePath := "/root/.cache/pip"
	cachePipMnt := llb.AddMount(
		pipCachePath,
		llb.Scratch(),
		llb.AsPersistentCacheDir(pipCachePath, llb.CacheMountLocked),
	)
	st = st.Run(llb.Shlex("pip install -r requirements.txt"), cachePipMnt).Root().
		File(llb.Copy(local, ".", "."))

	dt, err := st.Marshal(ctx, llb.LinuxAmd64)
	if err != nil {
		return nil, terror.Errorf(ctx, "marshal: %w", err)
	}

	return dt, nil
}
