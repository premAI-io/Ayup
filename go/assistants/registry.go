package assistants

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"go.opentelemetry.io/otel/attribute"

	"premai.io/Ayup/go/assistants/dockerfile"
	"premai.io/Ayup/go/assistants/exec"
	"premai.io/Ayup/go/assistants/extern"
	"premai.io/Ayup/go/assistants/python"
	"premai.io/Ayup/go/internal/assist"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
)

type Registry struct {
	table map[string]assist.Assistant
}

func NewRegistry() *Registry {
	table := make(map[string]assist.Assistant)
	table[assist.FullName(assist.Builtin, "dockerfile")] = &dockerfile.Assistant{}
	table[assist.FullName(assist.Builtin, "exec")] = &exec.Assistant{}
	table[assist.FullName(assist.Builtin, "python")] = &python.Assistant{}

	return &Registry{
		table: table,
	}
}

func (s *Registry) RegisterDir(ctx context.Context, kind assist.Kind, path string) (assist.Assistant, error) {
	bs, err := assist.LoadName(ctx, path)
	if err != nil {
		return nil, err
	}
	name := string(bs)

	fullName := assist.FullName(kind, name)
	trace.Event(ctx, "register assistant", attribute.String("name", fullName), attribute.String("path", path))

	if _, ok := s.table[fullName]; ok {
		return nil, terror.Errorf(ctx, "already exists: %s", fullName)
	}

	a := extern.New(kind, name, path)
	s.table[fullName] = a

	return a, nil
}

func (s *Registry) RegisterDirs(ctx context.Context, kind assist.Kind, path string) error {
	ctx, span := trace.Span(ctx, "register dirs")
	defer span.End()

	ents, err := os.ReadDir(path)
	if err != nil {
		return terror.Errorf(ctx, "os ReadDir: %w", err)
	}

	for _, ent := range ents {
		if !ent.Type().IsDir() {
			trace.Event(ctx, "skip non dir", attribute.String("name", ent.Name()))
			continue
		}

		if strings.HasPrefix(ent.Name(), ".") {
			trace.Event(ctx, "skip hidden", attribute.String("name", ent.Name()))
			continue
		}

		if _, err := s.RegisterDir(ctx, kind, filepath.Join(path, ent.Name())); err != nil {
			return err
		}
	}

	return nil
}

func (s *Registry) Get(ctx context.Context, name string) (assist.Assistant, error) {
	name = strings.TrimSpace(name)
	before, _, found := strings.Cut(name, ":")

	if !found {
		if name == "nil" {
			return nil, nil
		}

		return nil, terror.Errorf(ctx, "No ':' in assistant name: %s", name)
	}

	switch before {
	case string(assist.Builtin):
	case string(assist.Local):
	case string(assist.Remote):
	default:
		return nil, terror.Errorf(ctx, "Invalid assistant kind: %s", name)
	}

	assist, ok := s.table[name]
	if !ok {
		return nil, terror.Errorf(ctx, "assist.Assistant not found: %s", name)
	}
	return assist, nil
}

func (s *Registry) List() []assist.Assistant {
	list := make([]assist.Assistant, len(s.table))

	i := 0
	for _, v := range s.table {
		list[i] = v
		i += 1
	}

	return list
}

func (s *Registry) Del(fullName string) {
	delete(s.table, fullName)
}
