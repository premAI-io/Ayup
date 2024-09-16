package fs

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"go.opentelemetry.io/otel/attribute"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
)

func MkdirAll(ctx context.Context, pathParts ...string) error {
	if err := os.MkdirAll(filepath.Join(pathParts...), 0700); err != nil {
		return terror.Errorf(ctx, "os MkdirAll: %w", err)
	}

	return nil
}

func ReadFile(ctx context.Context, pathParts ...string) ([]byte, error) {
	path := filepath.Join(pathParts...)
	f, err := os.Open(path)

	if err != nil {
		if os.IsNotExist(err) {
			trace.Event(ctx, "file not exist", attribute.String("path", path))
			return nil, err
		}
		return nil, terror.Errorf(ctx, "open(%s): %w", path, err)
	}
	defer func() { terror.Ackf(ctx, "Close: %w", f.Close()) }()

	bs, err := io.ReadAll(f)
	if err != nil {
		return nil, terror.Errorf(ctx, "io ReadAll(%s): %w", path, err)
	}

	return bs, nil
}

func WriteFile(ctx context.Context, bs []byte, parts ...string) error {
	path := filepath.Join(parts...)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return terror.Errorf(ctx, "os Create(%s): %w", path, err)
	}

	_, err = f.Write(bs)
	if err != nil {
		return terror.Errorf(ctx, "f Write(%s): %w", path, err)
	}

	return nil
}

func ReadFileDefault(ctx context.Context, path string, def []byte) ([]byte, error) {
	bs, err := ReadFile(ctx, path)
	if err == nil {
		return bs, nil
	}

	if !os.IsNotExist(err) {
		return nil, err
	}

	if err := WriteFile(ctx, def, path); err != nil {
		return nil, err
	}

	return def, nil
}
