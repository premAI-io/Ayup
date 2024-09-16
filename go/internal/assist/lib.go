package assist

import (
	"bytes"
	"context"

	"premai.io/Ayup/go/internal/fs"
)

type Kind string

type Assistant interface {
	MayWork(Context, State) (bool, error)
	Assist(Context, State) (State, error)
	Name() string
}

const (
	Builtin Kind = "builtin"
	Local   Kind = "local"
	Remote  Kind = "remote"
)

func LoadName(ctx context.Context, assistantPath string) ([]byte, error) {
	bs, err := fs.ReadFile(ctx, assistantPath, "name")
	if err != nil {
		return nil, err
	}

	return bytes.TrimSpace(bs), err
}

func FullName(kind Kind, name string) string {
	return string(kind) + ":" + name
}

type Registry interface {
	Get(context.Context, string) (Assistant, error)
}
