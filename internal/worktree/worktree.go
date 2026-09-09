package worktree

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/safepath"
)

// Request fixes the only admitted workspace destination and branch for a run.
type Request struct {
	Version             int                 `json:"version"`
	RunID               string              `json:"run_id"`
	Source              repository.Identity `json:"source"`
	Path                string              `json:"path"`
	Branch              string              `json:"branch"`
	CandidateIdentity   string              `json:"candidate_identity,omitempty"`
	ControllerStateRoot string              `json:"controller_state_root,omitempty"`
}

// Prepare computes an immutable request without performing filesystem effects.
func Prepare(runID string, source repository.Identity) (Request, error) {
	r := Request{Version: 1, RunID: runID, Source: source, Path: filepath.Join(source.Root, ".harness", "worktrees", runID), Branch: "harness/" + runID}
	return r, r.Validate()
}

// Validate rejects destination substitution and malformed repository/run identity.
func (r Request) Validate() error {
	if err := r.Source.Validate(); err != nil {
		return err
	}
	if err := safepath.RequireDigest(r.RunID); err != nil {
		return err
	}
	if r.Version != 1 || r.Path != filepath.Join(r.Source.Root, ".harness", "worktrees", r.RunID) || r.Branch != "harness/"+r.RunID {
		return errors.New("worktree request substitution")
	}
	if r.CandidateIdentity != "" && r.CandidateIdentity != "semantic-index-v2" {
		return errors.New("unsupported candidate identity")
	}
	if r.ControllerStateRoot != "" {
		if !filepath.IsAbs(r.ControllerStateRoot) || filepath.Clean(r.ControllerStateRoot) != r.ControllerStateRoot {
			return errors.New("invalid controller state root")
		}
		for _, protected := range []string{r.Source.Root, filepath.Join(r.Source.Root, ".git"), r.Source.CommonDir, r.Path} {
			if pathsOverlap(r.ControllerStateRoot, protected) {
				return errors.New("controller state root overlaps repository or worktree state")
			}
		}
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	within := func(base, target string) bool {
		rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
		return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
	}
	return within(left, right) || within(right, left)
}

// Binding records the actual Git directory observed after creation.
type Binding struct {
	Request Request `json:"request"`
	GitDir  string  `json:"git_dir"`
}

// ID hashes the bound workspace; callers must Observe before trusting current state.
func (b Binding) ID() (string, error) {
	if err := b.Request.Validate(); err != nil {
		return "", err
	}
	if !filepath.IsAbs(b.GitDir) || filepath.Clean(b.GitDir) == filepath.Clean(b.Request.Source.CommonDir) {
		return "", errors.New("invalid worktree Git directory")
	}
	return canonical.Hash("harness.worktree.v1", b)
}

type blob struct{ mode, name, object string }

func inventory(ctx context.Context, r Request) ([]blob, error) {
	raw, err := git(ctx, r.Source.Root, "ls-tree", "-r", "-z", "--full-tree", r.Source.Commit)
	if err != nil {
		return nil, err
	}
	items := []blob{}
	seen := map[string]bool{}
	for _, line := range bytes.Split(raw, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		meta, name, ok := strings.Cut(string(line), "\t")
		parts := strings.Fields(meta)
		if !ok || len(parts) != 3 || parts[1] != "blob" || parts[0] != "100644" && parts[0] != "100755" {
			return nil, errors.New("unsupported source mode (symlink/submodule prohibited)")
		}
		if err := safepath.Relative(name); err != nil {
			return nil, err
		}
		for _, part := range strings.Split(name, "/") {
			if strings.EqualFold(part, ".git") {
				return nil, errors.New("Git control path in source tree")
			}
		}
		key := strings.ToLower(name)
		if seen[key] {
			return nil, errors.New("case-colliding source paths")
		}
		seen[key] = true
		items = append(items, blob{parts[0], name, parts[2]})
		if len(items) > 4096 {
			return nil, errors.New("workspace file count bound exceeded")
		}
	}
	return items, nil
}

// Create registers and populates an entirely new worktree. Preconditions: caller
// owns the lease and has persisted this exact intent. Any error after registration
// may leave partial state; never automatically call Create again to recover.
func Create(ctx context.Context, r Request) (Binding, error) {
	if err := r.Validate(); err != nil {
		return Binding{}, err
	}
	current, err := repository.Discover(ctx, r.Source.Root, r.Source.Name)
	if err != nil {
		return Binding{}, err
	}
	if current != r.Source {
		return Binding{}, errors.New("source identity changed")
	}
	items, err := inventory(ctx, r)
	if err != nil {
		return Binding{}, err
	}
	if err = safepath.EnsureDirectory(r.Source.Root, ".harness/worktrees"); err != nil {
		return Binding{}, err
	}
	if _, err = os.Lstat(r.Path); !os.IsNotExist(err) {
		return Binding{}, errors.New("destination already exists or cannot be inspected")
	}
	if _, err = git(ctx, r.Source.Root, "worktree", "add", "--no-checkout", "-b", r.Branch, r.Path, r.Source.Commit); err != nil {
		return Binding{}, err
	}
	if err = materialize(ctx, r, items); err != nil {
		return Binding{}, err
	}
	if _, err = git(ctx, r.Path, "read-tree", r.Source.Commit); err != nil {
		return Binding{}, err
	}
	return Observe(ctx, r)
}

func materialize(ctx context.Context, r Request, items []blob) error {
	if err := safepath.Directory(r.Path); err != nil {
		return err
	}
	root, err := os.OpenRoot(r.Path)
	if err != nil {
		return err
	}
	defer root.Close()
	return streamBlobs(ctx, r, items, func(item blob, reader io.Reader, size int64) error {
		parent := path.Dir(item.name)
		if parent != "." {
			if err := root.MkdirAll(parent, 0700); err != nil {
				return err
			}
		}
		if err := safepath.Check(root, item.name, true); err != nil {
			return err
		}
		mode := os.FileMode(0644)
		if item.mode == "100755" {
			mode = 0755
		}
		f, err := root.OpenFile(item.name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, err = io.CopyN(f, reader, size)
		if err == nil {
			err = f.Sync()
		}
		return errors.Join(err, f.Close())
	})
}

func streamBlobs(ctx context.Context, r Request, items []blob, visit func(blob, io.Reader, int64) error) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c := command(ctx, r.Source.Root, "cat-file", "--batch")
	var requests strings.Builder
	for _, item := range items {
		requests.WriteString(item.object + "\n")
	}
	c.Stdin = strings.NewReader(requests.String())
	var stderr capture
	c.Stderr = &stderr
	out, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	if err = c.Start(); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(out, 4096)
	total := int64(0)
	consume := func() error {
		for _, item := range items {
			header, err := reader.ReadSlice('\n')
			if err != nil {
				return err
			}
			if len(header) > 256 {
				return errors.New("oversized Git blob header")
			}
			parts := strings.Fields(string(header))
			if len(parts) != 3 || parts[0] != item.object || parts[1] != "blob" {
				return errors.New("Git blob identity mismatch")
			}
			size, err := strconv.ParseInt(parts[2], 10, 64)
			if err != nil || size < 0 || size > 64<<20 {
				return errors.New("Git blob size bound exceeded")
			}
			total += size
			if total > 512<<20 {
				return errors.New("workspace aggregate bound exceeded")
			}
			if err = visit(item, io.LimitReader(reader, size), size); err != nil {
				return err
			}
			end, err := reader.ReadByte()
			if err != nil || end != '\n' {
				return errors.New("Git blob terminator mismatch")
			}
		}
		extra, err := reader.ReadByte()
		if err != io.EOF {
			return fmt.Errorf("extra Git batch output %d: %v", extra, err)
		}
		return nil
	}
	err = consume()
	if err != nil {
		cancel()
	}
	waitErr := c.Wait()
	if stderr.overflow {
		return errors.New("Git stderr exceeded bound")
	}
	return errors.Join(err, waitErr)
}

// Observe validates destination, repository ancestry, HEAD, branch and reciprocal
// Git-directory registration without creating or repairing workspace state.
func Observe(ctx context.Context, r Request) (Binding, error) {
	if err := r.Validate(); err != nil {
		return Binding{}, err
	}
	if err := safepath.Directory(r.Path); err != nil {
		return Binding{}, err
	}
	root, err := os.OpenRoot(r.Path)
	if err != nil {
		return Binding{}, err
	}
	_, _, _, exists, err := safepath.ReadRegular(root, ".git", 4096)
	root.Close()
	if err != nil {
		return Binding{}, err
	}
	if !exists {
		return Binding{}, errors.New("worktree Git marker missing")
	}
	actual, err := repository.Discover(ctx, r.Path, r.Source.Name)
	if err != nil {
		return Binding{}, err
	}
	if filepath.Clean(actual.Root) != filepath.Clean(r.Path) || filepath.Clean(actual.CommonDir) != filepath.Clean(r.Source.CommonDir) || actual.Commit != r.Source.Commit || actual.Tree != r.Source.Tree || actual.ObjectFormat != r.Source.ObjectFormat {
		return Binding{}, errors.New("worktree repository/commit mismatch")
	}
	branch, err := gitText(ctx, r.Path, "symbolic-ref", "--short", "HEAD")
	if err != nil || branch != r.Branch {
		return Binding{}, errors.New("worktree branch mismatch")
	}
	dir, err := gitText(ctx, r.Path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return Binding{}, err
	}
	dir = filepath.Clean(dir)
	if err := safepath.Directory(dir); err != nil {
		return Binding{}, err
	}
	parent := filepath.Join(r.Source.CommonDir, "worktrees")
	rel, err := filepath.Rel(parent, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.ContainsAny(rel, "/\\") {
		return Binding{}, errors.New("Git directory outside worktree registry")
	}
	back, err := os.ReadFile(filepath.Join(dir, "gitdir"))
	if err != nil {
		return Binding{}, err
	}
	if filepath.Clean(strings.TrimSpace(string(back))) != filepath.Join(r.Path, ".git") {
		return Binding{}, errors.New("Git registration backlink mismatch")
	}
	b := Binding{Request: r, GitDir: dir}
	_, err = b.ID()
	return b, err
}
