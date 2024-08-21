package srv

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"go.opentelemetry.io/otel/attribute"
	tr "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"

	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/trace"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	gatewayapi "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	solverPb "github.com/moby/buildkit/solver/pb"
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

type aCtx struct {
	ctx       context.Context
	sendMutex *sync.Mutex
	stream    pb.Srv_AnalysisServer
	srv       *Srv
}

func (s *aCtx) span(name string, attrs ...attribute.KeyValue) (aCtx, tr.Span) {
	ctx, span := trace.Span(s.ctx, name, attrs...)

	return aCtx{
		ctx:       ctx,
		sendMutex: s.sendMutex,
		stream:    s.stream,
		srv:       s.srv,
	}, span
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

func (s *aCtx) useDockerfile() (bool, error) {
	_, err := os.Stat(filepath.Join(s.srv.SrcDir, "Dockerfile"))
	if err != nil {
		if !os.IsNotExist(err) {
			return false, s.internalError("stat Dockerfile: %w", err)
		}

		return false, nil
	}

	if err := s.send(&pb.ActReply{
		Source: "ayup",
		Variant: &pb.ActReply_Log{
			Log: "Found Dockerfile, will use it",
		},
	}); err != nil {
		return false, err
	}

	s.srv.push.analysis = &pb.AnalysisResult{
		UseDockerfile: true,
	}

	return true, nil
}

func (s *aCtx) execProcess(ctr gateway.Container, recvChan chan recvReq, source string, onLog func([]byte)) error {
	logWriter := logWriter{actx: s, source: source, onLog: onLog}

	if err := s.send(&pb.ActReply{
		Source: "ayup",
		Variant: &pb.ActReply_Log{
			Log: "Executing `python __main__.py`",
		},
	}); err != nil {
		return err
	}

	pid, err := ctr.Start(s.ctx, gateway.StartRequest{
		Cwd: "/app",
		// TODO: Run the Dockerfile's CMD or entrypoint
		Args:   []string{"python", "__main__.py"},
		Tty:    false,
		Stdout: &logWriter,
		Stderr: &logWriter,
	})
	if err != nil {
		return terror.Errorf(s.ctx, "ctr Start: %w", err)
	}

	waitChan := make(chan error)

	go func() {
		var retErr error

		if err := pid.Wait(); err != nil {
			var exitError *gatewayapi.ExitError
			if ok := errors.As(err, &exitError); ok {
				trace.Event(s.ctx, "Child exited",
					attribute.Int("exitCode", int(exitError.ExitCode)),
					attribute.String("error", exitError.Error()),
				)

				if exitError.ExitCode >= gatewayapi.UnknownExitStatus {
					retErr = exitError.Err
				}
			}
		}

		if retErr != nil {
			waitChan <- terror.Errorf(s.ctx, "pid Wait: %w", retErr)
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
		case req := <-recvChan:
			trace.Event(s.ctx, "Got user request")

			if req.err != nil {
				return req.err
			}
			if req.req.GetCancel() {
				trace.Event(s.ctx, "Got cancel", attribute.Int("count", cancelCount))

				switch cancelCount {
				case 0:
					if err := pid.Signal(s.ctx, syscall.SIGINT); err != nil {
						return terror.Errorf(s.ctx, "pid Signal: %w", err)
					}
				case 1:
					if err := pid.Signal(s.ctx, syscall.SIGTERM); err != nil {
						return terror.Errorf(s.ctx, "pid Signal: %w", err)
					}

				case 2:
					if err := pid.Signal(s.ctx, syscall.SIGKILL); err != nil {
						return terror.Errorf(s.ctx, "pid Signal: %w", err)
					}
				default:
					return terror.Errorf(s.ctx, "more than 3 cancel attempts")
				}
				cancelCount += 1
			} else {
				return terror.Errorf(s.ctx, "Unexpected message")
			}
		}
	}
}

type logWriter struct {
	actx   *aCtx
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
	trace.Event(s.actx.ctx, "log write", attribute.IntSlice("bytes", byteToIntSlice(p)))
	if err := s.actx.send(&pb.ActReply{
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

var ErrUserCancelled = errors.New("user cancelled")

func (s *aCtx) assistantLlb(secretsRunOpts []llb.RunOption) (*llb.Definition, error) {
	assLocal := llb.Local("assistant")
	assMnt := llb.AddMount("/assistant", assLocal)
	appLocal := llb.Local("app")
	appMnt := llb.AddMount("/in/app", appLocal, llb.Readonly)
	logMnt := llb.AddMount("/in/log", assLocal, llb.SourcePath("/in/log"), llb.Readonly)

	st := pythonSlimLlb().
		File(llb.Mkdir("/assistant", 0755)).
		File(llb.Mkdir("/in", 0755)).
		File(llb.Mkdir("/in/app", 0755)).
		File(llb.Mkdir("/out", 0755)).
		File(llb.Mkdir("/out/app", 0755))

	runOpts := []llb.RunOption{llb.Shlex("python /assistant/__main__.py")}
	runOpts = append(runOpts, secretsRunOpts...)
	runOpts = append(runOpts, assMnt, appMnt)

	if _, err := os.Stat(filepath.Join(s.srv.AssistantDir, "in", "log")); err == nil {
		runOpts = append(runOpts, logMnt)
	}

	st = st.File(llb.Copy(assLocal, "requirements.txt", "/assistant/requirements.txt"))
	st = pythonSlimPip(st, "install -r /assistant/requirements.txt").
		Run(runOpts...).Root()

	stOut := llb.Scratch().File(llb.Copy(st, "/out/*", "/", &llb.CopyInfo{
		AllowWildcard:  true,
		CreateDestPath: true,
	}))

	dt, err := stOut.Marshal(s.ctx, llb.LinuxAmd64)
	if err != nil {
		return nil, terror.Errorf(s.ctx, "marshal: %w", err)
	}

	return dt, nil
}

func (s *aCtx) callAssistant(c *client.Client) error {
	actx, span := s.span("assistant")
	defer span.End()
	ctx := actx.ctx

	providerMap, secretsRunOpts, err := actx.srv.loadAyupEnv(ctx, pb.Source_assistant)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	b := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		def, err := actx.assistantLlb(secretsRunOpts)
		if err != nil {
			return nil, actx.internalError("mkllb: %w", err)
		}

		r, err := c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, actx.internalError("client solve: %w", err)
		}

		return r, nil
	}

	assDir := actx.srv.AssistantDir
	srcDir := actx.srv.SrcDir
	statusChan := actx.buildkitStatusSender("assistant", nil)
	assistantFS, err := fsutil.NewFS(assDir)
	if err != nil {
		return actx.internalError("fsutil newfs: %w", err)
	}

	if err := os.MkdirAll(filepath.Join(assDir, "in"), 0700); err != nil {
		return actx.internalError("filepath Join: %w", err)
	}

	outDir := filepath.Join(assDir, "out")
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return actx.internalError("filepath Join: %w", err)
	}

	appFS, err := fsutil.NewFS(actx.srv.SrcDir)
	if err != nil {
		return actx.internalError("fsutil newfs: %w", err)
	}

	_, err = c.Build(ctx, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: outDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			"assistant": assistantFS,
			"app":       appFS,
		},
		Session: []session.Attachable{
			secretsprovider.FromMap(providerMap),
		},
	}, "ayup", b, statusChan)

	if err != nil {
		return actx.internalError("client build: %w", err)
	}

	if err := os.RemoveAll(srcDir); err != nil {
		return actx.internalError("os RemoveAll: %w", err)
	}

	if err := os.Rename(filepath.Join(outDir, "app"), srcDir); err != nil {
		return actx.internalError("os Rename: %w", err)
	}

	return nil
}

func (s *Srv) Analysis(stream pb.Srv_AnalysisServer) error {
	ctx := stream.Context()
	span := tr.SpanFromContext(ctx)
	ctx = trace.SetSpanKind(ctx, tr.SpanKindServer)

	actx := aCtx{
		ctx:       ctx,
		sendMutex: &sync.Mutex{},
		stream:    stream,
		srv:       s,
	}

	recvChan := make(chan recvReq)

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

			recvChan <- recvReq{req, err}

			if err == io.EOF {
				break
			}
		}
	}(ctx)

	if ok, err := s.checkPeerAuth(ctx); !ok || err != nil {
		if err != nil {
			return actx.internalError("checkPeerAuth: %w", err)
		}

		return actx.sendError("Not authorized")
	}

	c, err := client.New(ctx, s.BuildkitdAddr)
	if err != nil {
		return actx.internalError("client new: %w", err)
	}

	// TODO: Check if we are dealing with an existing session etc.
	r, ok := <-recvChan
	if !ok {
		return actx.internalError("stream recv: channel closed")
	}
	if r.err != nil {
		return actx.internalError("stream recv: %w", r.err)
	}

	if r.req.Cancel {
		return actx.sendError("analysis canceled")
	}

	if r.req.Choice != nil {
		return actx.sendError("premature choice")
	}

	var onLog func([]byte)
	if s.push.hasAssistant {
		if err := actx.callAssistant(c); err != nil {
			return err
		}

		ctxLogFile, err := os.OpenFile(filepath.Join(s.AssistantDir, "in", "log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return terror.Errorf(ctx, "os OpenFile: %w", err)
		}
		defer func() {
			terror.Ackf(ctx, "ctxLogFile: %w", ctxLogFile.Close())
		}()

		onLog = func(b []byte) {
			trace.Event(ctx, "onLog", attribute.Int("len", len(b)))
			_, err := ctxLogFile.Write(b)
			terror.Ackf(ctx, "ctxLogFile Write: %w", err)
		}
	}

	requirements_path := filepath.Join(s.SrcDir, "requirements.txt")

	if ok, err := actx.useDockerfile(); ok || err != nil {
		if err != nil {
			return err
		}
		actx, span := actx.span("dockerfile")
		defer span.End()

		b := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			r, err := c.Solve(ctx, gateway.SolveRequest{
				Frontend: "dockerfile.v0",
			})
			if err != nil {
				return nil, actx.internalError("gateway client solve: %w", err)
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
				return nil, actx.internalError("gateway client NewContainer: %w", err)
			}
			defer func() { terror.Ackf(ctx, "ctr Release: %w", ctr.Release(ctx)) }()

			if err := actx.execProcess(ctr, recvChan, "app", onLog); err != nil {
				return nil, err
			}

			return r, nil
		}

		contextFS, err := fsutil.NewFS(s.SrcDir)
		if err != nil {
			return actx.internalError("fsutil newfs: %w", err)
		}

		statusChan := actx.buildkitStatusSender("dockerfile", onLog)
		if _, err := c.Build(ctx, client.SolveOpt{
			LocalMounts: map[string]fsutil.FS{
				"dockerfile": contextFS,
				"context":    contextFS,
			},
		}, "ayup", b, statusChan); err != nil {
			return actx.internalError("build: %w", err)
		}

		return actx.send(&pb.ActReply{
			Variant: &pb.ActReply_AnalysisResult{
				AnalysisResult: &pb.AnalysisResult{
					UseDockerfile: true,
				},
			},
		})
	} else if _, err := os.Stat(requirements_path); err != nil {
		actx, span := actx.span("requirements")
		defer span.End()

		if !os.IsNotExist(err) {
			return actx.internalError("stat requirements.txt: %w", err)
		}

		span.AddEvent("No requirements.txt")
		err := actx.send(&pb.ActReply{
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
			return err
		}

		span.AddEvent("Waiting for choice")
		r, ok := <-recvChan
		if !ok {
			return actx.internalError("stream recv: channel closed")
		}
		if r.err != nil {
			return actx.internalError("stream recv: %w", r.err)
		}

		if r.req.Cancel {
			return actx.sendError("analysis canceled")
		}

		choice := r.req.Choice.GetBool()
		if choice == nil {
			return actx.sendError("expected choice for requirements.txt")
		} else if !choice.Value {
			return actx.sendError("can't continue without requirements.txt; please provide one!")
		}

		span.AddEvent("Creating requirements.txt")

		local := llb.Local("context", llb.ExcludePatterns([]string{".git"}))
		st := pythonSlimPip(pythonSlimLlb(), "install pipreqs").
			File(llb.Copy(local, ".", ".")).
			Run(llb.Shlex("pipreqs")).Root()

		dt, err := st.Marshal(ctx, llb.LinuxAmd64)
		if err != nil {
			return actx.internalError("marshal: %w", err)
		}

		b := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			r, err := c.Solve(ctx, gateway.SolveRequest{
				Definition: dt.ToPB(),
			})
			if err != nil {
				return nil, actx.internalError("client solve: %w", err)
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
			return actx.internalError("fsutil newfs: %w", err)
		}

		statusChan := actx.buildkitStatusSender("pipreqs", nil)
		if _, err := c.Build(ctx, client.SolveOpt{
			LocalMounts: map[string]fsutil.FS{
				"context": contextFS,
			},
		}, "ayup", b, statusChan); err != nil {
			return actx.internalError("build: %w", err)
		}

		span.End()
	} else {
		span.AddEvent("requirements.txt exists")

		if err := actx.send(&pb.ActReply{
			Source: "Ayup",
			Variant: &pb.ActReply_Log{
				Log: "requirements.txt found",
			},
		}); err != nil {
			return err
		}
	}

	s.push.analysis = &pb.AnalysisResult{
		UsePythonRequirements: true,
	}

	requirementsFile, err := os.OpenFile(requirements_path, os.O_RDONLY, 0)
	if err != nil {
		return actx.internalError("open file: %w", err)
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
		actx, span := actx.span("app")
		defer span.End()

		b := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			def, err := s.MkLlb(ctx)
			if err != nil {
				return nil, actx.internalError("mkllb: %w", err)
			}

			r, err := c.Solve(ctx, gateway.SolveRequest{
				Definition: def.ToPB(),
			})
			if err != nil {
				return nil, actx.internalError("client solve: %w", err)
			}

			ctr, err := c.NewContainer(ctx, gateway.NewContainerRequest{
				Hostname: "app",
				Mounts: []gateway.Mount{
					{
						Dest:      "/",
						MountType: solverPb.MountType_BIND,
						Ref:       r.Ref,
					},
				},
			})
			if err != nil {
				return nil, actx.internalError("gateway client NewContainer: %w", err)
			}
			defer func() { terror.Ackf(ctx, "ctr Release: %w", ctr.Release(ctx)) }()

			if err := actx.execProcess(ctr, recvChan, "app", onLog); err != nil {
				return nil, err
			}

			return r, nil
		}

		statusChan := actx.buildkitStatusSender("build", onLog)
		contextFS, err := fsutil.NewFS(s.SrcDir)
		if err != nil {
			return actx.internalError("fsutil newfs: %w", err)
		}

		_, err = c.Build(ctx, client.SolveOpt{
			LocalMounts: map[string]fsutil.FS{
				"context": contextFS,
			},
		}, "ayup", b, statusChan)

		if err != nil {
			return actx.internalError("client build: %w", err)
		}

		return nil
	}(); err != nil {
		return err
	}

	return actx.send(&pb.ActReply{})
}

func pythonSlimLlb() llb.State {
	return llb.Image("docker.io/library/python:3.12-slim").
		AddEnv("PYTHONUNBUFFERED", "True").
		File(llb.Mkdir("/app", 0755)).
		Dir("/app").
		File(llb.Rm("/etc/apt/apt.conf.d/docker-clean"))
}

func pythonSlimPip(st llb.State, args string, ro ...llb.RunOption) llb.State {
	pipCachePath := "/root/.cache/pip"
	cachePipMnt := llb.AddMount(
		pipCachePath,
		llb.Scratch(),
		llb.AsPersistentCacheDir(pipCachePath, llb.CacheMountLocked),
	)

	ro = append([]llb.RunOption{llb.Shlexf("pip %s", args), cachePipMnt}, ro...)

	return st.Run(ro...).Root()
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

func (s *aCtx) buildkitStatusSender(source string, onLog func([]byte)) chan *client.SolveStatus {
	statusChan := make(chan *client.SolveStatus)
	sendLog := func(text string) {
		_ = s.send(&pb.ActReply{
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

				trace.Event(s.ctx, "buildkit log",
					attribute.String("text", string(log.Data)),
					attribute.IntSlice("bytes", byteToIntSlice(log.Data)),
				)

				sendLog(string(log.Data))
			}

		}
	}()

	return statusChan
}
