package assist

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client/llb"
	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/exp/constraints"

	"premai.io/Ayup/go/internal/fs"
	"premai.io/Ayup/go/internal/semver"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
)

// The state and configuration of an app, saved in the .ayup/ dir
type State struct {
	SrcPath  string
	Path     string
	registry Registry

	version  semver.Version
	buildDef *llb.Definition
	platform llb.ConstraintsOpt

	first      bool
	next       Assistant
	workingDir string
	cmd        []string
	ports      []uint32
}

func NewState(srcPath string, path string, registry Registry) State {
	return State{
		SrcPath:  srcPath,
		Path:     path,
		registry: registry,
		platform: llb.Platform(platforms.Normalize(platforms.DefaultSpec())),
		first:    true,
	}
}

func (s State) Join(parts ...string) string {
	return filepath.Join(s.Path, filepath.Join(parts...))
}

func (s State) MkdirAll(ctx context.Context, parts ...string) error {
	path := s.Join(parts...)
	if err := os.MkdirAll(path, 0700); err != nil {
		return terror.Errorf(ctx, "os MkdirAll(%s): %w", path, err)
	}
	trace.Event(ctx, "make dir", attribute.String("path", path))

	return nil
}

func (s State) writeFile(ctx context.Context, bs []byte, parts ...string) error {
	return fs.WriteFile(ctx, bs, s.Join(parts...))
}

func (s State) readFile(ctx context.Context, parts ...string) ([]byte, error) {
	return fs.ReadFile(ctx, s.Join(parts...))
}

func (s State) Version(ctx context.Context) (semver.Version, State, error) {
	var v semver.Version
	if !s.version.IsZero() {
		return s.version, s, nil
	}

	bs, err := fs.ReadFileDefault(ctx, s.Join("version"), semver.GetAyupVersion().Bytes())
	if err != nil {
		return v, s, terror.Errorf(ctx, "ioutil ReadFile: %w", err)
	}

	v, err = semver.Parse(bs)
	if err != nil {
		return v, s, terror.Errorf(ctx, "parseVer: %w", err)
	}

	s.version = v

	return v, s, nil
}

func (s State) SetNext(ctx context.Context, next Assistant) (State, error) {
	if next == nil {
		trace.Event(ctx, "state set next nil")
	} else {
		trace.Event(ctx, "state set next", attribute.String("next", next.Name()))
	}
	s.next = next

	name := "nil"
	if next != nil {
		name = next.Name()
	}

	return s, s.writeFile(ctx, []byte(name), "next")
}

func (s State) SetBuildDef(ctx context.Context, def *llb.Definition) (State, error) {
	s.buildDef = def

	return s, nil
}

func (s State) SetWorkingDir(ctx context.Context, path string) (State, error) {
	s.workingDir = path

	return s, s.writeFile(ctx, []byte(path), "workingdir")
}

func (s State) SetCmd(ctx context.Context, cmd []string) (State, error) {
	trace.Event(ctx, "state set cmd", attribute.StringSlice("cmd", cmd))
	s.cmd = cmd

	bs, err := json.Marshal(cmd)
	if err != nil {
		return s, terror.Errorf(ctx, "json Marshal: %w", err)
	}

	return s, s.writeFile(ctx, bs, "cmd")
}

func (s State) SetPorts(ctx context.Context, ports []uint32) (State, error) {
	s.ports = ports

	bs, err := json.Marshal(ports)
	if err != nil {
		return s, terror.Errorf(ctx, "json Marshal: %w", err)
	}

	return s, s.writeFile(ctx, bs, "ports")
}

func portSliceCast[T1 constraints.Integer, T2 constraints.Integer](ctx context.Context, in []T1) (out []T2, err error) {
	out = make([]T2, len(in))

	for i, k := range in {
		if int(k) > 65535 || int(k) < 1 {
			err = terror.Errorf(ctx, "%d is outside the port number range", k)
		}
		out[i] = T2(k)
	}

	return
}

func (s *State) clearStale(ctx context.Context) error {
	if err := s.writeFile(ctx, []byte("nil"), "next"); err != nil {
		return err
	}

	return nil
}

func (s *State) loadNextAssistant(ctx context.Context, from string) error {
	bs, err := s.readFile(ctx, from)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	if err != nil {
		return nil
	}

	old := "nil"
	if s.next != nil {
		old = s.next.Name()
	}
	novus := string(bs)

	trace.Event(
		ctx,
		"load state",
		attribute.String("name", from),
		attribute.String("old", old),
		attribute.String("new", novus),
	)

	a, err := s.registry.Get(ctx, novus)
	if err != nil {
		return err
	}
	s.next = a

	return nil
}

func (s State) LoadState(ctx context.Context) (State, error) {
	if s.first {
		s.first = false

		if err := s.clearStale(ctx); err != nil {
			return s, err
		}
		if err := s.loadNextAssistant(ctx, "first"); err != nil {
			return s, err
		}
	} else {
		if err := s.loadNextAssistant(ctx, "next"); err != nil {
			return s, err
		}
	}

	bs, err := s.readFile(ctx, "cmd")
	if err != nil && !os.IsNotExist(err) {
		return s, err
	}

	if err == nil {
		var cmd []string
		if err := json.Unmarshal(bs, &cmd); err != nil {
			return s, terror.Errorf(ctx, "json Unmarshal: %w", err)
		}

		trace.Event(
			ctx,
			"load state",
			attribute.String("name", "cmd"),
			attribute.StringSlice("old", s.cmd),
			attribute.StringSlice("new", cmd),
		)

		s.cmd = cmd
	}

	bs, err = s.readFile(ctx, "workingdir")
	if err != nil && !os.IsNotExist(err) {
		return s, err
	}

	if err == nil {
		trace.Event(
			ctx,
			"load state",
			attribute.String("name", "workingdir"),
			attribute.String("old", s.workingDir),
			attribute.String("new", string(bs)),
		)
		s.workingDir = string(bs)
	}

	oldPorts, err := portSliceCast[uint32, int](ctx, s.ports)
	if err != nil {
		return s, err
	}

	bs, err = s.readFile(ctx, "ports")
	if err != nil && !os.IsNotExist(err) {
		return s, err
	}

	if err == nil {
		var ports []int
		if err := json.Unmarshal(bs, &ports); err != nil {
			return s, terror.Errorf(ctx, "json Unmarshal: %w", err)
		}

		trace.Event(
			ctx,
			"load state",
			attribute.String("name", "ports"),
			attribute.IntSlice("old", oldPorts),
			attribute.IntSlice("new", ports),
		)

		s.ports, err = portSliceCast[int, uint32](ctx, ports)
		if err != nil {
			return s, err
		}
	}

	return s, nil
}

func (s State) GetNext() Assistant {
	return s.next
}

func (s State) GetBuildDef() *llb.Definition {
	return s.buildDef
}

func (s State) GetPlatform() llb.ConstraintsOpt {
	return s.platform
}

func (s State) GetWorkingDir() string {
	if s.workingDir == "" {
		return "/app"
	}

	return s.workingDir
}

func (s State) GetCmd() []string {
	return s.cmd
}

func (s State) GetPorts() []uint32 {
	return s.ports
}
