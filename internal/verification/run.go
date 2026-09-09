package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"harness.local/engorch/internal/safepath"
)

// Stream describes captured output. Truncated means Hash covers only CapturedBytes,
// not complete process output; Excerpt is a bounded, UTF-8-safe rendering.
type Stream struct {
	Hash          string `json:"hash"`
	CapturedBytes int64  `json:"captured_bytes"`
	ObservedBytes int64  `json:"observed_bytes"`
	Truncated     bool   `json:"truncated"`
	Excerpt       string `json:"excerpt"`
}

// Result records process facts, not candidate freshness or run readiness.
type Result struct {
	Version          int    `json:"version"`
	InvocationID     string `json:"invocation_id"`
	CandidateID      string `json:"candidate_id"`
	Status           string `json:"status"`
	Started          bool   `json:"started"`
	Start            string `json:"start"`
	End              string `json:"end"`
	ExitCode         *int   `json:"exit_code"`
	TimedOut         bool   `json:"timed_out"`
	Cancelled        bool   `json:"cancelled"`
	TerminationScope string `json:"termination_scope"`
	Stdout           Stream `json:"stdout"`
	Stderr           Stream `json:"stderr"`
	Error            string `json:"error"`
}

type sink struct {
	digest             hash.Hash
	captured, observed int64
	excerpt            []byte
	overflow           bool
	cancel             context.CancelFunc
}

// Write captures a bounded prefix and cancels on overflow while draining pipes.
func (s *sink) Write(b []byte) (int, error) {
	n := len(b)
	s.observed += int64(n)
	left := (4 << 20) - s.captured
	if int64(len(b)) > left {
		b = b[:left]
		s.overflow = true
		s.cancel()
	}
	s.digest.Write(b)
	s.captured += int64(len(b))
	if len(s.excerpt) < 8192 {
		take := 8192 - len(s.excerpt)
		if take > len(b) {
			take = len(b)
		}
		s.excerpt = append(s.excerpt, b[:take]...)
	}
	return n, nil
}

func (s *sink) stream() Stream {
	excerpt := strings.ToValidUTF8(string(s.excerpt), "�")
	if len(excerpt) > 8192 {
		excerpt = excerpt[:8192]
		for !utf8.ValidString(excerpt) {
			excerpt = excerpt[:len(excerpt)-1]
		}
	}
	return Stream{Hash: hex.EncodeToString(s.digest.Sum(nil)), CapturedBytes: s.captured, ObservedBytes: s.observed, Truncated: s.overflow, Excerpt: excerpt}
}

// Execute runs exactly one prepared invocation. Callers must persist its intent
// first. It honors cancellation, bounds output and never converts missing tools
// or incomplete output into PASS. It does not automatically retry any command.
func Execute(ctx context.Context, i Invocation) (Result, error) {
	id, err := i.ID()
	if err != nil {
		return Result{}, err
	}
	r := Result{Version: 1, InvocationID: id, CandidateID: i.CandidateID, Status: "NOT_RUN", Start: time.Now().UTC().Format(time.RFC3339Nano), TerminationScope: terminationScope()}
	deadline, cancel := context.WithTimeout(ctx, time.Duration(i.Check.TimeoutSeconds)*time.Second)
	defer cancel()
	limited, stop := context.WithCancel(deadline)
	defer stop()
	out := &sink{digest: sha256.New(), cancel: stop}
	stderr := &sink{digest: sha256.New(), cancel: stop}
	finish := func() Result {
		r.TimedOut = r.TimedOut || errors.Is(deadline.Err(), context.DeadlineExceeded)
		r.Cancelled = r.Cancelled || ctx.Err() != nil
		if r.Started && (r.TimedOut || r.Cancelled || out.overflow || stderr.overflow) {
			r.Status = "FAIL"
		}
		r.End = time.Now().UTC().Format(time.RFC3339Nano)
		r.Stdout = out.stream()
		r.Stderr = stderr.stream()
		return r
	}
	if err := ctx.Err(); err != nil {
		r.Cancelled = true
		r.Error = "cancelled before process start"
		return finish(), nil
	}
	if i.Executable == nil {
		r.Error = i.Unavailable
		return finish(), nil
	}
	if err := safepath.Directory(i.Directory); err != nil {
		r.Error = "working directory failed admission"
		return finish(), nil
	}
	hash, err := binaryHash(i.Executable.Path)
	if err != nil || hash != i.Executable.Hash {
		r.Error = "executable identity changed before dispatch"
		return finish(), nil
	}
	cmd := exec.CommandContext(limited, i.Executable.Path, i.Check.Argv[1:]...)
	cmd.Dir = i.Directory
	cmd.Env = i.env()
	cmd.Stdout = out
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second
	configureProcess(cmd)
	if err := cmd.Start(); err != nil {
		r.Error = "process could not start"
		return finish(), nil
	}
	r.Started = true
	err = cmd.Wait()
	if cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		r.ExitCode = &code
	}
	r.TimedOut = errors.Is(deadline.Err(), context.DeadlineExceeded)
	r.Cancelled = ctx.Err() != nil
	r.Status = "FAIL"
	if err == nil && !r.TimedOut && !r.Cancelled && !out.overflow && !stderr.overflow {
		r.Status = "PASS"
	}
	if err != nil {
		r.Error = "verification process failed"
	}
	if out.overflow || stderr.overflow {
		r.Error = "verification output exceeded capture limit"
	}
	return finish(), nil
}

// ValidateResult rejects impossible success and identity substitution. The
// controller separately validates candidate observations before promoting READY.
func ValidateResult(i Invocation, r Result) error {
	id, err := i.ID()
	if err != nil {
		return err
	}
	if r.Version != 1 || r.InvocationID != id || r.CandidateID != i.CandidateID {
		return errors.New("verification result identity mismatch")
	}
	start, err := time.Parse(time.RFC3339Nano, r.Start)
	if err != nil {
		return err
	}
	end, err := time.Parse(time.RFC3339Nano, r.End)
	if err != nil || end.Before(start) {
		return errors.New("invalid verification time interval")
	}
	for _, s := range []Stream{r.Stdout, r.Stderr} {
		if err := safepath.RequireDigest(s.Hash); err != nil {
			return err
		}
		if s.CapturedBytes < 0 || s.CapturedBytes > 4<<20 || s.ObservedBytes < s.CapturedBytes || s.ObservedBytes > 9007199254740991 || len(s.Excerpt) > 8192 || !utf8.ValidString(s.Excerpt) || !s.Truncated && s.ObservedBytes != s.CapturedBytes {
			return errors.New("invalid output receipt")
		}
	}
	want := "NOT_RUN"
	if r.TerminationScope != "windows-best-effort-process-tree" && r.TerminationScope != "unix-process-group" {
		return errors.New("unknown termination scope")
	}
	if r.Started {
		if i.Executable == nil {
			return errors.New("process started without resolved executable")
		}
		want = "FAIL"
		if r.ExitCode != nil && *r.ExitCode == 0 && !r.TimedOut && !r.Cancelled && !r.Stdout.Truncated && !r.Stderr.Truncated && r.Error == "" {
			want = "PASS"
		}
	} else if r.ExitCode != nil || r.Error == "" {
		return errors.New("invalid not-run receipt")
	}
	if !r.Started && (r.Stdout.ObservedBytes != 0 || r.Stderr.ObservedBytes != 0) {
		return errors.New("output without process start")
	}
	if r.Status != want {
		return errors.New("verification status not supported by process facts")
	}
	return nil
}
