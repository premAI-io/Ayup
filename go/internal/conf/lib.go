package conf

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"premai.io/Ayup/go/internal/terror"

	"github.com/joho/godotenv"
)

func confFilePath(ctx context.Context) (string, error) {
	confDir, err := os.UserConfigDir()
	if err != nil {
		return "", terror.Errorf(ctx, "os UserConfigDir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(confDir, "ayup"), 0700); err != nil {
		return "", terror.Errorf(ctx, "os MkdirAll: %w", err)
	}

	return filepath.Join(confDir, "ayup", "env"), nil
}

func read(ctx context.Context, path string) (confMap map[string]string, err error) {
	confMap, err = godotenv.Read(path)
	if errors.Is(err, os.ErrNotExist) {
		confMap = make(map[string]string)
	} else if err != nil {
		return nil, terror.Errorf(ctx, "godotenv Read: %w", err)
	}

	return confMap, nil
}

func write(ctx context.Context, path string, confMap map[string]string) error {
	text, err := godotenv.Marshal(confMap)
	if err != nil {
		return terror.Errorf(ctx, "godotenv Marshal: %w", err)
	}

	file, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_TRUNC|os.O_WRONLY,
		0600,
	)
	if err != nil {
		return terror.Errorf(ctx, "os OpenFile: %w", err)
	}
	defer file.Close()

	if _, err = file.WriteString(text); err != nil {
		return terror.Errorf(ctx, "file WriteString: %w", err)
	}

	return nil
}

func Append(ctx context.Context, key string, val string) error {
	path, err := confFilePath(ctx)
	if err != nil {
		return err
	}

	confMap, err := read(ctx, path)
	if err != nil {
		return err
	}

	oldVal, ok := confMap[key]
	if !ok || oldVal == "" {
		confMap[key] = val
	} else {
		confMap[key] = fmt.Sprintf("%s,%s", oldVal, val)
	}

	return write(ctx, path, confMap)
}

func Set(ctx context.Context, key string, val string) error {
	path, err := confFilePath(ctx)
	if err != nil {
		return err
	}

	confMap, err := read(ctx, path)
	if err != nil {
		return err
	}

	confMap[key] = val

	return write(ctx, path, confMap)
}
