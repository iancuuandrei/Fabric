package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/gitexec"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

type candidateDiff struct {
	RunID       string       `json:"run_id"`
	CandidateID string       `json:"candidate_id"`
	Workspace   string       `json:"workspace"`
	Diff        string       `json:"diff"`
	Coverage    diffCoverage `json:"coverage"`
}

// diffCoverage states exactly which workspace state the diff covers. The
// candidate fingerprint may include ignored bytes, but the diff never reads
// ignored files: untracked enumeration uses --exclude-standard, so ignored
// paths (which may hold secrets) are excluded without being opened.
type diffCoverage struct {
	Tracked   string `json:"tracked"`
	Untracked string `json:"untracked"`
	Ignored   string `json:"ignored"`
}

func diffCoverageV1() diffCoverage {
	return diffCoverage{
		Tracked:   "git diff HEAD: tracked modifications against the admitted commit",
		Untracked: "git ls-files --others --exclude-standard: non-ignored untracked files only",
		Ignored:   "excluded without reading: ignored files never enter the diff, though the candidate fingerprint may include ignored bytes",
	}
}

// diffCommand observes the existing isolated workspace under a shared read
// lease. It never reuses a file proposal or runs a provider. The read lease on
// the exact workspace request is held across the initial capture, every Git
// observation and the drift recapture; the lease is released before any
// successful output is emitted.
func diffCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) > 1 {
		return errors.New("diff accepts at most one RUN")
	}
	id := ""
	if len(args) == 1 {
		id = args[0]
	} else {
		var err error
		id, err = latestRunID(root)
		if err != nil {
			return err
		}
	}
	p, err := runPath(root, id)
	if err != nil {
		return err
	}
	s, err := control.Inspect(p)
	if err != nil {
		return err
	}
	if err := requireRunBinding(s, root, id); err != nil {
		return err
	}
	if s.Workspace == nil {
		return errors.New("run has no isolated workspace")
	}
	lease, err := worktree.AcquireRead(s.Workspace.Request)
	if err != nil {
		return err
	}
	var result candidateDiff
	observeErr := lease.WithOwnership(s.Workspace.Request, func(worktree.LeaseIdentity) error {
		candidate, _, err := worktree.Capture(ctx, *s.Workspace)
		if err != nil {
			return err
		}
		if err := candidate.ValidateBinding(*s.Workspace); err != nil {
			return err
		}
		candidateID, err := candidate.ID()
		if err != nil {
			return err
		}
		diff, err := workspaceDiff(ctx, s.Workspace.Request.Path)
		if err != nil {
			return err
		}
		// External editors bypass the read lease, so recapture under the same
		// lease hold and reject any drift from the initially captured ID.
		after, _, err := worktree.Capture(ctx, *s.Workspace)
		if err != nil {
			return err
		}
		if err := after.ValidateBinding(*s.Workspace); err != nil {
			return err
		}
		afterID, err := after.ID()
		if err != nil {
			return err
		}
		if err := requireStableCandidate(candidateID, afterID); err != nil {
			return err
		}
		result = candidateDiff{RunID: id, CandidateID: candidateID, Workspace: s.Workspace.Request.Path, Diff: diff, Coverage: diffCoverageV1()}
		return nil
	})
	// Release the lease before emitting successful output. A close failure
	// fails first so guard substitution is never hidden behind output success.
	if err := joinCloseFirst(lease, observeErr); err != nil {
		return err
	}
	// Raw byte bounds are insufficient because JSON escaping expands the
	// payload, so admit the exact encoded size with a clear bound error.
	if err := checkDiffEncodedBound(result); err != nil {
		return err
	}
	return output(out, result)
}

// joinCloseFirst releases the held read lease before any output is emitted
// and reports the close error ahead of any observation error.
func joinCloseFirst(lease *worktree.ReadLease, observeErr error) error {
	if closeErr := lease.Close(); closeErr != nil {
		return errors.Join(closeErr, observeErr)
	}
	return observeErr
}

// requireStableCandidate rejects drift between the pre- and post-observation
// captures. External editors bypass the read lease, so identity comparison is
// the only drift signal.
func requireStableCandidate(beforeID, afterID string) error {
	if beforeID != afterID {
		return errors.New("candidate changed during diff observation; retry the diff")
	}
	return nil
}

// checkDiffEncodedBound admits the exact canonical JSON encoding of the diff
// result. A raw payload near the byte bound can exceed canonical.MaxBytes
// after JSON escaping, so admission uses the encoded size.
func checkDiffEncodedBound(result candidateDiff) error {
	if !utf8.ValidString(result.Diff) {
		return errors.New("workspace diff is not valid UTF-8")
	}
	encoded, err := canonical.Bytes(result)
	if err != nil {
		return errors.New("workspace diff encoded output exceeds 1 MiB bound")
	}
	if len(encoded) > canonical.MaxBytes {
		return errors.New("workspace diff encoded output exceeds 1 MiB bound")
	}
	return nil
}

func workspaceDiff(ctx context.Context, workspace string) (string, error) {
	tracked, err := gitOutput(ctx, workspace, false, "diff", "--no-ext-diff", "--binary", "HEAD", "--")
	if err != nil {
		return "", err
	}
	if len(tracked) > canonical.MaxBytes {
		return "", errors.New("workspace diff exceeds 1 MiB bound")
	}
	rawPaths, err := gitOutput(ctx, workspace, false, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	paths := strings.Split(strings.TrimSuffix(rawPaths, "\x00"), "\x00")
	sort.Strings(paths)
	var b strings.Builder
	b.WriteString(tracked)
	for _, path := range paths {
		if path == "" {
			continue
		}
		// Git accepts /dev/null as the empty side on all supported platforms.
		// Only this --no-index form may report differences via exit code 1.
		// Ignored paths never reach this point: --exclude-standard above
		// enumerates non-ignored untracked files only, and they are the only
		// files opened here.
		piece, err := gitOutput(ctx, workspace, true, "diff", "--no-index", "--binary", "--", "/dev/null", filepath.FromSlash(path))
		if err != nil {
			return "", err
		}
		if b.Len()+len(piece) > canonical.MaxBytes {
			return "", errors.New("workspace diff exceeds 1 MiB bound")
		}
		b.WriteString(piece)
	}
	if b.Len() > canonical.MaxBytes {
		return "", errors.New("workspace diff exceeds 1 MiB bound")
	}
	return b.String(), nil
}

// boundedCapture drains subprocess output while retaining at most
// canonical.MaxBytes+1 bytes; overflow is reported as a bound error at
// admission instead of truncating silently.
type boundedCapture struct {
	buffer   bytes.Buffer
	overflow bool
}

// Write drains output while retaining only the bounded diagnostic prefix.
func (c *boundedCapture) Write(p []byte) (int, error) {
	n := len(p)
	remaining := canonical.MaxBytes + 1 - c.buffer.Len()
	if remaining < 0 {
		remaining = 0
	}
	if len(p) > remaining {
		c.overflow = true
		p = p[:remaining]
	}
	c.buffer.Write(p)
	return n, nil
}

// These thin aliases keep the existing CLI resolver tests stable while the
// implementation and production command path live in the shared package.
const gitExecutableEnv = gitexec.ExecutableEnv

func defaultGitCandidates(goos string) []string { return gitexec.DefaultCandidates(goos) }

func resolveGitExecutable(getenv func(string) string, stat func(string) (os.FileInfo, error), evalSymlinks func(string) (string, error), candidates []string) (string, error) {
	return gitexec.ResolveExecutable(getenv, stat, evalSymlinks, candidates)
}

// gitOutput runs one Git command with bounded stdout/stderr. Exit code 1 is
// accepted only when allowExitOne is set for `diff --no-index`, which uses it
// to report a real difference; every other command treats any failure as an
// error. Context cancellation is preserved even when stderr is empty.
func gitOutput(ctx context.Context, dir string, allowExitOne bool, args ...string) (string, error) {
	// Read-only observations must never refresh the index stat cache: the v1
	// candidate identity hashes the raw index bytes, so any refresh would
	// false-trigger drift recapture on an otherwise quiescent workspace.
	// git diff refreshes stat cache explicitly even with --no-optional-locks,
	// so both tracked and --no-index diff invocations pin
	// diff.autoRefreshIndex=false. Optional-lock controls stay in place.
	argv := []string{"--no-optional-locks", "-C", dir}
	if len(args) > 0 && args[0] == "diff" {
		argv = append([]string{"-c", "diff.autoRefreshIndex=false"}, argv...)
	}
	argv = append(argv, args...)
	command := gitexec.CommandContext(ctx, argv...)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stdout, stderr boundedCapture
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if stdout.overflow || stderr.overflow {
		return "", errors.New("git output exceeds 1 MiB bound")
	}
	if err != nil {
		var exit *exec.ExitError
		if allowExitOne && errors.As(err, &exit) && exit.ExitCode() == 1 {
			return stdout.buffer.String(), nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		detail := strings.TrimSpace(stderr.buffer.String())
		if detail == "" {
			return "", fmt.Errorf("git %s failed: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("git %s failed: %s: %w", strings.Join(args, " "), detail, err)
	}
	return stdout.buffer.String(), nil
}
