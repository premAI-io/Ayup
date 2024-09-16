package extern

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/tonistiigi/fsutil"
	"go.opentelemetry.io/otel/attribute"

	"premai.io/Ayup/go/internal/assist"
	"premai.io/Ayup/go/internal/terror"
)

type Assistant struct {
	kind assist.Kind
	name string

	selfPath string
}

var _ assist.Assistant = (*Assistant)(nil)

func New(kind assist.Kind, name string, path string) *Assistant {
	return &Assistant{
		kind:     kind,
		name:     name,
		selfPath: path,
	}
}

func (s *Assistant) Name() string {
	return assist.FullName(s.kind, s.name)
}

func (s *Assistant) MayWork(assist.Context, assist.State) (bool, error) {
	return true, nil
}

func (s *Assistant) genRunLlb(ctx assist.Context, st llb.State, cmd []string, secretsRunOpts []llb.RunOption) (*llb.Definition, error) {
	appLocal := llb.Local("app")
	stateLocal := llb.Local("state")
	appMnt := llb.AddMount("/in/app", appLocal, llb.Readonly)
	stateMnt := llb.AddMount("/in/state", stateLocal, llb.Readonly)

	st = st.
		File(llb.Mkdir("/in", 0755)).
		File(llb.Mkdir("/in/state", 0755)).
		File(llb.Mkdir("/in/app", 0755)).
		File(llb.Mkdir("/out", 0755)).
		File(llb.Mkdir("/out/state", 0755)).
		File(llb.Mkdir("/out/app", 0755))

	runOpts := []llb.RunOption{llb.Args(cmd), appMnt, stateMnt}
	runOpts = append(runOpts, secretsRunOpts...)

	st = st.Run(runOpts...).Root()

	stOut := llb.Scratch().File(llb.Copy(st, "/out/*", "/", &llb.CopyInfo{
		AllowWildcard:  true,
		CreateDestPath: true,
	}))

	def, err := stOut.Marshal(ctx.Ctx, llb.LinuxAmd64)
	if err != nil {
		return nil, terror.Errorf(ctx.Ctx, "marshal: %w", err)
	}

	return def, nil
}

func (s *Assistant) Assist(aCtx assist.Context, state assist.State) (assist.State, error) {
	aCtx, span := aCtx.Span("Assist", attribute.String("self path", s.selfPath), attribute.String("app path", aCtx.AppPath))
	defer span.End()

	providerMap, secretsRunOpts, err := assist.LoadEnv(aCtx.Ctx, filepath.Join(s.selfPath, ".ayup-env"))
	if err != nil && !os.IsNotExist(err) {
		return state, err
	}

	b := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		r, err := c.Solve(ctx, gateway.SolveRequest{
			Frontend: "dockerfile.v0",
		})
		if err != nil {
			return nil, terror.Errorf(ctx, "gateway client solve: %w", err)
		}

		config, ok := r.Metadata[exptypes.ExporterImageConfigKey]
		if !ok {
			return nil, terror.Errorf(ctx, "%s not found in metadata", exptypes.ExporterImageConfigKey)
		}

		var img dockerspec.DockerOCIImage
		if err := json.Unmarshal(config, &img); err != nil {
			return nil, err
		}

		cmd := img.Config.Entrypoint
		cmd = append(cmd, img.Config.Cmd...)

		if len(cmd) < 1 {
			return nil, terror.Errorf(aCtx.Ctx, "No ENTRYPOINT or CMD in Dockerfile")
		}

		ref, err := r.SingleRef()
		if err != nil {
			return nil, terror.Errorf(ctx, "r SingleRef: %w", err)
		}

		st, err := ref.ToState()
		if err != nil {
			return nil, terror.Errorf(ctx, "ref ToState: %w", err)
		}

		def, err := s.genRunLlb(aCtx, st, cmd, secretsRunOpts)
		if err != nil {
			return nil, err
		}

		r, err = c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, terror.Errorf(ctx, "client solve: %w", err)
		}

		return r, nil
	}

	assistantFS, err := fsutil.NewFS(s.selfPath)
	if err != nil {
		return state, terror.Errorf(aCtx.Ctx, "fsutil newfs: %w", err)
	}

	outDir := filepath.Join(aCtx.ScratchPath, "extern")
	if err := os.RemoveAll(outDir); err != nil {
		return state, terror.Errorf(aCtx.Ctx, "os RemoveAll: %w", err)
	}
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return state, terror.Errorf(aCtx.Ctx, "os MkdirAll: %w", err)
	}

	appFS, err := fsutil.NewFS(aCtx.AppPath)
	if err != nil {
		return state, terror.Errorf(aCtx.Ctx, "fsutil newfs: %w", err)
	}

	stateFS, err := fsutil.NewFS(aCtx.StatePath)
	if err != nil {
		return state, terror.Errorf(aCtx.Ctx, "fsutil NewFS: %w", err)
	}

	if aCtx.Client == nil {
		return state, terror.Errorf(aCtx.Ctx, "client is nil: %v", aCtx)
	}

	_, err = aCtx.Client.Build(aCtx.Ctx, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: outDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			"dockerfile": assistantFS,
			"context":    assistantFS,
			"app":        appFS,
			"state":      stateFS,
		},
		Session: []session.Attachable{
			secretsprovider.FromMap(providerMap),
		},
	}, "ayup", b, aCtx.BuildkitStatusSender(s.Name(), nil))

	if err != nil {
		return state, terror.Errorf(aCtx.Ctx, "client build: %w", err)
	}

	if err := os.RemoveAll(aCtx.AppPath); err != nil {
		return state, terror.Errorf(aCtx.Ctx, "os RemoveAll: %w", err)
	}

	if err := os.Rename(filepath.Join(outDir, "app"), aCtx.AppPath); err != nil {
		return state, terror.Errorf(aCtx.Ctx, "os Rename: %w", err)
	}

	ents, err := os.ReadDir(filepath.Join(outDir, "state"))
	if err != nil {
		return state, terror.Errorf(aCtx.Ctx, "os ReadDir: %w", err)
	}

	for _, ent := range ents {
		if ent.IsDir() {
			continue
		}
		if !ent.Type().IsRegular() {
			continue
		}

		if strings.HasPrefix(ent.Name(), ".") {
			continue
		}

		baseName := ent.Name()
		newPath := filepath.Join(aCtx.StatePath, baseName)
		if err := os.Remove(newPath); err != nil {
			return state, terror.Errorf(aCtx.Ctx, "os Remove: %w", err)
		}

		if err := os.Rename(filepath.Join(outDir, "state", baseName), newPath); err != nil {
			return state, terror.Errorf(aCtx.Ctx, "os Rename: %w", err)
		}
	}

	return state.LoadState(aCtx.Ctx)
}
