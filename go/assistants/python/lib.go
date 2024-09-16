package python

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/tonistiigi/fsutil"

	"premai.io/Ayup/go/assistants/exec"
	"premai.io/Ayup/go/internal/assist"
	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
)

type Assistant struct {
	requirementsPath string
	hasRequirements  bool
}

var _ assist.Assistant = (*Assistant)(nil)

func (s *Assistant) Name() string {
	return assist.FullName(assist.Builtin, "python")
}

func sendLog(aCtx assist.Context, msg string) error {
	if err := aCtx.Send(&pb.ActReply{
		Source: "python",
		Variant: &pb.ActReply_Log{
			Log: msg,
		},
	}); err != nil {
		return err
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

func (s *Assistant) MayWork(aCtx assist.Context, _ assist.State) (bool, error) {
	aCtx, span := aCtx.Span("python MayWork")
	defer span.End()

	s.requirementsPath = filepath.Join(aCtx.AppPath, "requirements.txt")
	_, err := os.Stat(s.requirementsPath)
	if err == nil {
		s.hasRequirements = true

		if err := sendLog(aCtx, "requirements.txt exists"); err != nil {
			return false, err
		}

		return true, nil
	}

	if !os.IsNotExist(err) {
		return false, terror.Errorf(aCtx.Ctx, "stat requirements.txt: %w", err)
	}

	span.AddEvent("No requirements.txt")

	_, err = os.Stat(filepath.Join(aCtx.AppPath, "__main__.py"))
	if err != nil {
		if !os.IsNotExist(err) {
			return false, terror.Errorf(aCtx.Ctx, "state __main__.py: %w", err)
		}
		if err := sendLog(aCtx, "no __main__.py"); err != nil {
			return false, err
		}

		span.AddEvent("no __main__.py")
		return false, nil
	}

	return true, nil
}

func (s *Assistant) pipreqs(aCtx assist.Context, state assist.State) error {
	aCtx, span := aCtx.Span("pipreqs")
	defer span.End()

	err := aCtx.Send(&pb.ActReply{
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

	trace.Event(aCtx.Ctx, "Waiting for choice")
	r, ok := <-aCtx.RecvChan
	if !ok {
		return terror.Errorf(aCtx.Ctx, "stream recv: channel closed")
	}
	if r.Err != nil {
		return terror.Errorf(aCtx.Ctx, "stream recv: %w", r.Err)
	}

	if r.Req.Cancel {
		return terror.Errorf(aCtx.Ctx, "analysis canceled")
	}

	choice := r.Req.Choice.GetBool()
	if choice == nil {
		return terror.Errorf(aCtx.Ctx, "expected choice for requirements.txt")
	} else if !choice.Value {
		return terror.Errorf(aCtx.Ctx, "can't continue without requirements.txt; please provide one!")
	}

	trace.Event(aCtx.Ctx, "Creating requirements.txt")

	local := llb.Local("context", llb.ExcludePatterns([]string{".git"}))
	st := pythonSlimPip(pythonSlimLlb(), "install pipreqs").
		File(llb.Copy(local, ".", ".")).
		Run(llb.Shlex("pipreqs")).Root()

	dt, err := st.Marshal(aCtx.Ctx, state.GetPlatform())
	if err != nil {
		return terror.Errorf(aCtx.Ctx, "marshal: %w", err)
	}

	b := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		r, err := c.Solve(ctx, gateway.SolveRequest{
			Definition: dt.ToPB(),
		})
		if err != nil {
			return nil, terror.Errorf(aCtx.Ctx, "client solve: %w", err)
		}

		reqs, err := r.Ref.ReadFile(ctx, gateway.ReadRequest{
			Filename: "/app/requirements.txt",
		})
		if err != nil {
			return nil, terror.Errorf(ctx, "ref readfile: %w", err)
		}

		requirementsFile, err := os.OpenFile(s.requirementsPath, os.O_CREATE|os.O_WRONLY, 0666)
		if err != nil {
			return nil, terror.Errorf(ctx, "openfile requirements: %w", err)
		}
		defer requirementsFile.Close()

		if _, err := requirementsFile.Write(reqs); err != nil {
			return nil, terror.Errorf(ctx, "requirementsFile write: %w", err)
		}

		return r, nil
	}

	contextFS, err := fsutil.NewFS(aCtx.AppPath)
	if err != nil {
		return terror.Errorf(aCtx.Ctx, "fsutil newfs: %w", err)
	}

	if _, err := aCtx.Client.Build(aCtx.Ctx, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			"context": contextFS,
		},
	}, "ayup", b, aCtx.BuildkitStatusSender("pipreqs", nil)); err != nil {
		return terror.Errorf(aCtx.Ctx, "build: %w", err)
	}

	return nil
}

func (s *Assistant) Assist(aCtx assist.Context, state assist.State) (assist.State, error) {
	aCtx, span := aCtx.Span("python")
	defer span.End()

	if !s.hasRequirements {
		if err := s.pipreqs(aCtx, state); err != nil {
			return state, err
		}
	}

	requirementsFile, err := os.OpenFile(s.requirementsPath, os.O_RDONLY, 0)
	if err != nil {
		return state, terror.Errorf(aCtx.Ctx, "open file: %w", err)
	}
	defer requirementsFile.Close()

	local := llb.Local("context", llb.ExcludePatterns([]string{".venv", ".git"}))
	st := pythonSlimLlb()

	aptDeps := []string{}
	gitRegex := regexp.MustCompile(`@\s+git`)
	opencvRegex := regexp.MustCompile(`^\s*opencv-python\b`)
	lines := bufio.NewScanner(requirementsFile)
	for lines.Scan() {
		line := lines.Text()

		if gitRegex.MatchString(line) {
			aptDeps = append(aptDeps, "git")
		}

		if opencvRegex.MatchString(line) {
			aptDeps = append(aptDeps, "libgl1")
			aptDeps = append(aptDeps, "libglib2.0-0")
		}
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

	def, err := st.Marshal(aCtx.Ctx, llb.LinuxAmd64)
	if err != nil {
		return state, terror.Errorf(aCtx.Ctx, "marshal: %w", err)
	}

	state, err = state.SetBuildDef(aCtx.Ctx, def)
	if err != nil {
		return state, err
	}

	state, err = state.SetWorkingDir(aCtx.Ctx, "/app")
	if err != nil {
		return state, err
	}

	state, err = state.SetCmd(aCtx.Ctx, []string{"python", "__main__.py"})
	if err != nil {
		return state, err
	}

	state, err = state.SetPorts(aCtx.Ctx, []uint32{5000})
	if err != nil {
		return state, err
	}

	return state.SetNext(aCtx.Ctx, &exec.Assistant{})
}
