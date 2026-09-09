package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"harness.local/engorch/internal/canonical"
)

// Identity binds a named checkout and Git object identity. Paths are local
// observations, not portable evidence of sandbox containment.
type Identity struct {
	Version      int    `json:"version"`
	Name         string `json:"name"`
	Root         string `json:"root"`
	CommonDir    string `json:"common_dir"`
	ObjectFormat string `json:"object_format"`
	Commit       string `json:"commit"`
	Tree         string `json:"tree"`
}

// Validate checks the v1 shape without claiming the paths still exist.
func (i Identity) Validate() error {
	if i.Version != 1 || strings.TrimSpace(i.Name) == "" || len(i.Name) > 256 || !filepath.IsAbs(i.Root) || !filepath.IsAbs(i.CommonDir) {
		return errors.New("invalid repository identity")
	}
	n := 40
	if i.ObjectFormat == "sha256" {
		n = 64
	} else if i.ObjectFormat != "sha1" {
		return errors.New("unsupported Git object format")
	}
	for _, id := range []string{i.Commit, i.Tree} {
		if len(id) != n || strings.Trim(id, "0123456789abcdef") != "" {
			return errors.New("invalid Git object ID")
		}
	}
	return nil
}

// ID hashes a validated immutable observation. It does not re-read Git.
func (i Identity) ID() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash("harness.repository.v1", i)
}

type bounded struct {
	bytes.Buffer
	overflow bool
}

// Write drains subprocess output while retaining only the bounded prefix. The
// caller rejects overflow after process completion instead of accepting truncation.
func (b *bounded) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (64 << 10) - b.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.overflow = true
	}
	b.Buffer.Write(p)
	return n, nil
}

func git(ctx context.Context, root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	argv := append([]string{"--no-optional-locks", "--no-replace-objects", "-C", root}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.WaitDelay = time.Second
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	var out, stderr bounded
	cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1")
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if out.overflow || stderr.overflow {
		return "", errors.New("Git output bound exceeded")
	}
	if err != nil {
		return "", fmt.Errorf("Git observation: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Discover resolves HEAD once, then derives its tree from that exact commit.
// It performs no fetch or mutation and bounds every subprocess to ten seconds.
func Discover(ctx context.Context, path, name string) (Identity, error) {
	i := Identity{Version: 1, Name: name}
	var err error
	i.Root, err = git(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return i, err
	}
	i.Root = filepath.Clean(i.Root)
	i.CommonDir, err = git(ctx, i.Root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return i, err
	}
	i.CommonDir = filepath.Clean(i.CommonDir)
	i.ObjectFormat, err = git(ctx, i.Root, "rev-parse", "--show-object-format")
	if err != nil {
		return i, err
	}
	i.Commit, err = git(ctx, i.Root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return i, err
	}
	i.Tree, err = git(ctx, i.Root, "rev-parse", "--verify", i.Commit+"^{tree}")
	if err != nil {
		return i, err
	}
	return i, i.Validate()
}

// DiscoverCommit observes one exact full commit ID in the selected repository.
// It never checks out, fetches or resolves a mutable branch name supplied as input.
func DiscoverCommit(ctx context.Context, path, name, commit string) (Identity, error) {
	i, err := Discover(ctx, path, name)
	if err != nil {
		return Identity{}, err
	}
	n := 40
	if i.ObjectFormat == "sha256" {
		n = 64
	}
	if len(commit) != n || strings.Trim(commit, "0123456789abcdef") != "" {
		return Identity{}, errors.New("exact full lowercase commit ID required")
	}
	resolved, err := git(ctx, i.Root, "rev-parse", "--verify", commit+"^{commit}")
	if err != nil {
		return Identity{}, err
	}
	if resolved != commit {
		return Identity{}, errors.New("requested object is not the exact commit")
	}
	i.Commit = commit
	i.Tree, err = git(ctx, i.Root, "rev-parse", "--verify", commit+"^{tree}")
	if err != nil {
		return Identity{}, err
	}
	return i, i.Validate()
}
