package state

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"premai.io/Ayup/go/internal/fs"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/tui"
)

func HasAyup(ctx context.Context, path string) error {
	if _, err := os.Stat(filepath.Join(path, ".ayup")); err != nil {
		if !os.IsNotExist(err) {
			return terror.Errorf(ctx, "os Stat: %w", err)
		}

		return terror.Errorf(ctx, ".ayup state directory not found, have you run Ayup on this project yet?")
	}

	return nil
}

func ShowAssistant(ctx context.Context, path string) error {
	bs, err := fs.ReadFile(ctx, path, ".ayup", "first")
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	name := string(bs)
	if err != nil || name == "nil" {
		fmt.Println("No first assistant set. One will be selected at runtime.")
		return nil
	}

	fmt.Println(tui.TitleStyle.Render("Assistant:"), string(bs))

	return nil
}

func SetAssistant(ctx context.Context, path string, name string) error {
	if name != "nil" && strings.Count(name, ":") != 1 {
		return terror.Errorf(ctx, "Assistant name does not appear to be valid, it should have the type of assistant (builtin, local or remote) followed by a ':' and the name name like 'builtin:dockerfile'")
	}

	if err := fs.MkdirAll(ctx, path, ".ayup"); err != nil {
		return err
	}

	if err := fs.WriteFile(ctx, []byte(name), path, ".ayup", "first"); err != nil {
		return err
	}

	fmt.Println(tui.TitleStyle.Render("Set Assistant!"), name)

	return nil
}
