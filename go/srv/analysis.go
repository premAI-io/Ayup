package srv

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	tr "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"

	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/trace"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/opencontainers/go-digest"

	// gatewayapi "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/tonistiigi/fsutil"

	"premai.io/Ayup/go/internal/terror"
)

type ActServer interface {
	Send(*pb.ActReply) error
	Recv() (*pb.ActReq, error)
	grpc.ServerStream
}

func (s *Srv) useDockerfile(ctx context.Context, stream pb.Srv_AnalysisServer) (bool, error) {
	internalError := mkInternalError(ctx, stream)

	_, err := os.Stat(filepath.Join(s.SrcDir, "Dockerfile"))
	if err != nil {
		if !os.IsNotExist(err) {
			return false, internalError("stat Dockerfile: %w", err)
		}

		return false, nil
	}

	if err := stream.Send(&pb.ActReply{
		Source: "ayup",
		Variant: &pb.ActReply_Log{
			Log: "Found Dockerfile, will use it",
		},
	}); err != nil {
		return false, internalError("stream send: %w", err)
	}

	s.push.analysis = &pb.AnalysisResult{
		UseDockerfile: true,
	}

	return true, nil
}

func (s *Srv) Analysis(stream pb.Srv_AnalysisServer) error {
	ctx := stream.Context()
	span := tr.SpanFromContext(ctx)
	ctx = trace.SetSpanKind(ctx, tr.SpanKindServer)

	sendError := mkSendError(ctx, stream)
	internalError := mkInternalError(ctx, stream)

	recvChan := mkRecvChan(ctx, stream)

	if ok, err := s.checkPeerAuth(ctx); !ok || err != nil {
		if err != nil {
			return internalError("checkPeerAuth: %w", err)
		}

		return sendError("Not authorized")
	}

	c, err := client.New(ctx, s.BuildkitdAddr)
	if err != nil {
		return internalError("client new: %w", err)
	}

	// TODO: Check if we are dealing with an existing session etc.
	r, ok := <-recvChan
	if !ok {
		return internalError("stream recv: channel closed")
	}
	if r.err != nil {
		return internalError("stream recv: %w", r.err)
	}

	if r.req.Cancel {
		return sendError("analysis canceled")
	}

	if r.req.Choice != nil {
		return sendError("premature choice")
	}

	requirements_path := filepath.Join(s.SrcDir, "requirements.txt")

	if ok, err := s.useDockerfile(ctx, stream); ok || err != nil {
		if err != nil {
			return err
		}
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

		in, out := startProc(ctx, cmd)

		return procWait("buildctl", in, out)
	} else if _, err := os.Stat(requirements_path); err != nil {
		ctx, span := trace.Span(ctx, "requirements")
		defer span.End()

		if !os.IsNotExist(err) {
			return internalError("stat requirements.txt: %w", err)
		}

		span.AddEvent("No requirements.txt")
		err := stream.Send(&pb.ActReply{
			Source: "ayup",
			Variant: &pb.ActReply_Choice{
				Choice: &pb.Choice{
					Variant: &pb.Choice_Bool{
						Bool: &pb.ChoiceBool{
							Value:       true,
							Title:       "No requirements.txt; try guessing it?",
							Description: "Guess what dependencies the program has by inspecting the source code.",
							Affirmative: "Yes, guess",
							Negative:    "No, I'll make it",
						},
					},
				},
			},
		})
		if err != nil {
			return internalError("stream send: %w", err)
		}

		span.AddEvent("Waiting for choice")
		r, ok := <-recvChan
		if !ok {
			return internalError("stream recv: channel closed")
		}
		if r.err != nil {
			return internalError("stream recv: %w", r.err)
		}

		if r.req.Cancel {
			return sendError("analysis canceled")
		}

		choice := r.req.Choice.GetBool()
		if choice == nil {
			return sendError("expected choice for requirements.txt")
		} else if !choice.Value {
			return sendError("can't continue without requirements.txt; please provide one!")
		}

		span.AddEvent("Creating requirements.txt")

		local := llb.Local("context", llb.ExcludePatterns([]string{".git"}))
		st := pythonSlimPip(pythonSlimLlb(), "install pipreqs").
			File(llb.Copy(local, ".", ".")).
			Run(llb.Shlex("pipreqs")).Root()

		dt, err := st.Marshal(ctx, llb.LinuxAmd64)
		if err != nil {
			return internalError("marshal: %w", err)
		}

		b := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			r, err := c.Solve(ctx, gateway.SolveRequest{
				Definition: dt.ToPB(),
			})
			if err != nil {
				return nil, internalError("client solve: %w", err)
			}

			reqs, err := r.Ref.ReadFile(ctx, gateway.ReadRequest{
				Filename: "/app/requirements.txt",
			})
			if err != nil {
				return nil, terror.Errorf(ctx, "ref readfile: %w", err)
			}

			requirementsFile, err := os.OpenFile(requirements_path, os.O_CREATE|os.O_WRONLY, 0666)
			if err != nil {
				return nil, terror.Errorf(ctx, "openfile requirements: %w", err)
			}
			defer requirementsFile.Close()

			if _, err := requirementsFile.Write(reqs); err != nil {
				return nil, terror.Errorf(ctx, "requirementsFile write: %w", err)
			}

			return r, nil
		}

		contextFS, err := fsutil.NewFS(s.SrcDir)
		if err != nil {
			return internalError("fsutil newfs: %w", err)
		}

		statusChan := buildkitStatusSender(ctx, stream)
		if _, err := c.Build(ctx, client.SolveOpt{
			LocalMounts: map[string]fsutil.FS{
				"context": contextFS,
			},
		}, "ayup", b, statusChan); err != nil {
			return internalError("build: %w", err)
		}
	} else {
		span.AddEvent("requirements.txt exists")

		if err := stream.Send(&pb.ActReply{
			Source: "Ayup",
			Variant: &pb.ActReply_Log{
				Log: "requirements.txt found",
			},
		}); err != nil {
			return internalError("stream send: %w", err)
		}
	}

	s.push.analysis = &pb.AnalysisResult{
		UsePythonRequirements: true,
	}

	requirementsFile, err := os.OpenFile(requirements_path, os.O_RDONLY, 0)
	if err != nil {
		return internalError("open file: %w", err)
	}
	defer requirementsFile.Close()

	gitRegex := regexp.MustCompile(`@\s+git`)
	opencvRegex := regexp.MustCompile(`^\s*opencv-python\b`)
	lines := bufio.NewScanner(requirementsFile)
	for lines.Scan() {
		line := lines.Text()

		if gitRegex.MatchString(line) {
			s.push.analysis.NeedsGit = true
		}

		if opencvRegex.MatchString(line) {
			s.push.analysis.NeedsLibGL = true
			s.push.analysis.NeedsLibGlib = true
		}
	}

	if err = func() (err error) {
		ctx, span := trace.Span(ctx, "build")
		defer span.End()

		internalError := mkInternalError(ctx, stream)

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

		statusChan := buildkitStatusSender(ctx, stream)
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
		return err
	}

	if err := stream.Send(&pb.ActReply{}); err != nil {
		return terror.Errorf(ctx, "stream send: %w", err)
	}

	return nil
}

func pythonSlimLlb() llb.State {
	return llb.Image("docker.io/library/python:3.12-slim").
		AddEnv("PYTHONUNBUFFERED", "True").
		File(llb.Mkdir("/app", 0755)).
		Dir("/app").
		File(llb.Rm("/etc/apt/apt.conf.d/docker-clean"))
}

func pythonSlimPip(st llb.State, args string) llb.State {
	pipCachePath := "/root/.cache/pip"
	cachePipMnt := llb.AddMount(
		pipCachePath,
		llb.Scratch(),
		llb.AsPersistentCacheDir(pipCachePath, llb.CacheMountLocked),
	)

	return st.Run(llb.Shlexf("pip %s", args), cachePipMnt).Root()
}

func (s *Srv) MkLlb(ctx context.Context) (*llb.Definition, error) {
	local := llb.Local("context", llb.ExcludePatterns([]string{".venv", ".git"}))
	st := pythonSlimLlb()

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

func buildkitStatusSender(ctx context.Context, stream pb.Srv_AnalysisServer) chan *client.SolveStatus {
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

			var prevLog *client.VertexLog
			for _, log := range msg.Logs {
				vertNo, ok := verts[log.Vertex]
				if !ok {
					vertNo = -1
				}

				if prevLog != nil && prevLog.Vertex == log.Vertex && prevLog.Timestamp == log.Timestamp {
					continue
				}
				prevLog = log

				text := strings.Trim(string(log.Data), "\r\n")
				for _, line := range strings.Split(text, "\n") {
					sendLog("buildkit", fmt.Sprintf("#%d %6s %s", vertNo, "LOG", line))
				}
			}

		}
	}()

	return statusChan
}
