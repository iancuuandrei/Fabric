package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type capture struct {
	bytes.Buffer
	overflow bool
}

// Write drains output while bounding retained bytes; overflow is an error at admission.
func (b *capture) Write(p []byte) (int, error) {
	n := len(p)
	left := (4 << 20) - b.Len()
	if len(p) > left {
		p = p[:left]
		b.overflow = true
	}
	b.Buffer.Write(p)
	return n, nil
}

func command(ctx context.Context, root string, args ...string) *exec.Cmd {
	argv := []string{"--no-optional-locks", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "core.splitIndex=false", "-C", root}
	argv = append(argv, args...)
	c := exec.CommandContext(ctx, "git", argv...)
	c.WaitDelay = time.Second
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			c.Env = append(c.Env, entry)
		}
	}
	c.Env = append(c.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	return c
}

func git(ctx context.Context, root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := command(ctx, root, args...)
	var out, stderr capture
	c.Stdout = &out
	c.Stderr = &stderr
	err := c.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if out.overflow || stderr.overflow {
		return nil, errors.New("Git output bound exceeded")
	}
	if err != nil {
		return nil, fmt.Errorf("Git: %w: %s", err, stderr.String())
	}
	return out.Bytes(), nil
}

func gitText(ctx context.Context, root string, args ...string) (string, error) {
	b, err := git(ctx, root, args...)
	return strings.TrimSpace(string(b)), err
}
