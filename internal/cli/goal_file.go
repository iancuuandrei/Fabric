package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func planObjective(ctx context.Context, root string, args []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(args) == 1 && args[0] != "--file" {
		return args[0], nil
	}
	if len(args) != 2 || args[0] != "--file" || args[1] == "" {
		return "", errors.New("plan requires one quoted objective or --file PATH")
	}
	path := args[1]
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 256<<10 {
		return "", errors.New("goal file must be a regular file of at most 256 KiB")
	}
	body, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(body) > 256<<10 || !utf8.Valid(body) || strings.TrimSpace(string(body)) == "" {
		return "", errors.New("goal file must contain nonempty UTF-8 text of at most 256 KiB")
	}
	return string(body), nil
}
