package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// diffWorkspaceFixture plans, approves and starts one run so diffCommand has an
// admitted isolated workspace to observe.
func diffWorkspaceFixture(t *testing.T) (root, runID, workspace string) {
	t.Helper()
	root = autonomousCLIFixture(t)
	var planned control.Snapshot
	if err := json.Unmarshal(mustExecuteCLI(t, root, "plan", "diff fixture objective"), &planned); err != nil {
		t.Fatal(err)
	}
	mustExecuteCLI(t, root, "approve", planned.RunID, planned.PlanID, "fixture-human")
	mustExecuteCLI(t, root, "run", planned.RunID)
	var admitted control.Snapshot
	if err := json.Unmarshal(mustExecuteCLI(t, root, "inspect", planned.RunID), &admitted); err != nil {
		t.Fatal(err)
	}
	if admitted.Workspace == nil {
		t.Fatal("workspace was not admitted")
	}
	return root, planned.RunID, admitted.Workspace.Request.Path
}

// mustDiffWithWorkspaceFiles admits a workspace, writes the given workspace
// files, runs diff and returns the bound candidate result. Callers keep all
// outcome/coverage assertions.
func mustDiffWithWorkspaceFiles(t *testing.T, files map[string]string) (string, string, string, candidateDiff) {
	t.Helper()
	root, runID, workspace := diffWorkspaceFixture(t)
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := Execute(context.Background(), []string{"diff", runID}, root, &out); err != nil {
		t.Fatal(err)
	}
	var result candidateDiff
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return root, runID, workspace, result
}

func TestDiffIncludesUntrackedPaths(t *testing.T) {
	root, runID, workspace, result := mustDiffWithWorkspaceFiles(t, map[string]string{
		"source.txt":         "fixture\nmodified\n",
		"untracked-note.txt": "untracked\n",
	})
	if result.RunID != runID || result.CandidateID == "" || result.Workspace != workspace {
		t.Fatalf("diff lost workspace binding: %#v", result)
	}
	if !strings.Contains(result.Diff, "untracked-note.txt") || !strings.Contains(result.Diff, "untracked") {
		t.Fatalf("untracked path missing from diff: %q", result.Diff)
	}
	if !strings.Contains(result.Diff, "source.txt") {
		t.Fatalf("tracked modification missing from diff: %q", result.Diff)
	}
	if result.Coverage.Tracked == "" || result.Coverage.Untracked == "" || result.Coverage.Ignored == "" {
		t.Fatalf("diff omitted coverage metadata: %#v", result.Coverage)
	}
	lease, err := worktree.AcquireRead(mustInspectWorkspace(t, root, runID).Request)
	if err != nil {
		t.Fatalf("read lease not acquirable after diff: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustInspectWorkspace(t *testing.T, root, runID string) worktree.Binding {
	t.Helper()
	p, err := runPath(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	s, err := control.Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Workspace == nil {
		t.Fatal("workspace missing")
	}
	return *s.Workspace
}

func TestDiffHoldsReadLeaseAgainstWriterContention(t *testing.T) {
	root, runID, _ := diffWorkspaceFixture(t)
	binding := mustInspectWorkspace(t, root, runID)
	writer, err := worktree.Acquire(binding.Request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	var out bytes.Buffer
	err = Execute(context.Background(), []string{"diff", runID}, root, &out)
	if err == nil {
		t.Fatal("diff succeeded while the writer lease was held")
	}
	if !errors.Is(err, worktree.ErrLeaseContention) && !strings.Contains(strings.ToLower(err.Error()), "lease") {
		t.Fatalf("diff did not report lease contention: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("contentious diff emitted partial output: %q", out.String())
	}
}

func TestDiffCoverageExcludesIgnoredFilesWithoutReading(t *testing.T) {
	// .gitignore is itself untracked (and therefore visible); the ignored
	// secret must never be opened, so neither its name nor its bytes may leak.
	_, _, _, result := mustDiffWithWorkspaceFiles(t, map[string]string{
		".gitignore":         "ignored-secret.txt\n",
		"ignored-secret.txt": "credential=must-not-be-printed\n",
	})
	// The visible .gitignore legitimately names the ignored file, so only a
	// diff section for the ignored file itself (git header paths carry a b/
	// prefix) or its secret bytes count as a leak.
	if strings.Contains(result.Diff, "b/ignored-secret.txt") || strings.Contains(result.Diff, "must-not-be-printed") {
		t.Fatalf("ignored file leaked into diff: %q", result.Diff)
	}
	if !strings.Contains(result.Diff, ".gitignore") {
		t.Fatalf("non-ignored untracked file missing from diff: %q", result.Diff)
	}
	if !strings.Contains(strings.ToLower(result.Coverage.Ignored), "exclud") || !strings.Contains(strings.ToLower(result.Coverage.Untracked), "non-ignored") {
		t.Fatalf("coverage metadata does not describe the ignored exclusion: %#v", result.Coverage)
	}
}

func TestDiffReleasesLeaseBeforeEmittingOutput(t *testing.T) {
	root, runID, _ := diffWorkspaceFixture(t)
	binding := mustInspectWorkspace(t, root, runID)
	probe := &emitLeaseProbe{request: binding.Request}
	if err := diffCommand(context.Background(), root, []string{runID}, probe); err != nil {
		t.Fatal(err)
	}
	if probe.probeErr != nil {
		t.Fatalf("lease probe failed: %v", probe.probeErr)
	}
	if !probe.acquired {
		t.Fatal("read lease was still held while diff output was emitted")
	}
	var result candidateDiff
	if err := json.Unmarshal(probe.buffer.Bytes(), &result); err != nil {
		t.Fatalf("probe captured invalid diff output: %v", err)
	}
	if result.RunID != runID || result.CandidateID == "" {
		t.Fatalf("probe captured unbound diff output: %#v", result)
	}
}

// emitLeaseProbe attempts an exclusive writer lease on every output write: it
// succeeds only if diffCommand already released its read lease before emission.
type emitLeaseProbe struct {
	request  worktree.Request
	buffer   bytes.Buffer
	acquired bool
	probeErr error
}

// Write probes exclusive lease availability on every output write: it
// succeeds only if diffCommand already released its read lease before emission.
func (w *emitLeaseProbe) Write(p []byte) (int, error) {
	writer, err := worktree.Acquire(w.request)
	if err != nil {
		w.probeErr = err
		return w.buffer.Write(p)
	}
	w.acquired = true
	w.probeErr = writer.Close()
	return w.buffer.Write(p)
}

func TestDiffCloseErrorReportedFirst(t *testing.T) {
	root, runID, _ := diffWorkspaceFixture(t)
	binding := mustInspectWorkspace(t, root, runID)
	lease, err := worktree.AcquireRead(binding.Request)
	if err != nil {
		t.Fatal(err)
	}
	guardPath := worktree.LeaseGuardPath(binding.Request)
	if err := os.WriteFile(guardPath, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	err = joinCloseFirst(lease, errors.New("observation boom"))
	if err := os.WriteFile(guardPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err == nil || !strings.Contains(err.Error(), "observation boom") {
		t.Fatalf("observation error lost after close failure: %v", err)
	}
	closeIdx := strings.Index(err.Error(), "lease guard")
	boomIdx := strings.Index(err.Error(), "observation boom")
	if closeIdx < 0 || boomIdx < 0 || closeIdx > boomIdx {
		t.Fatalf("close error did not fail first: %v", err)
	}
}

func TestDiffEncodedBoundAccountsForJSONEscaping(t *testing.T) {
	// Every quote doubles under JSON escaping, so this raw payload fits the
	// byte bound while its encoding exceeds canonical.MaxBytes.
	raw := strings.Repeat("\"", canonical.MaxBytes/2)
	if len(raw) >= canonical.MaxBytes {
		t.Fatal("escaping fixture does not fit the raw bound")
	}
	oversized := candidateDiff{
		RunID:       strings.Repeat("a", 64),
		CandidateID: strings.Repeat("b", 64),
		Workspace:   "workspace",
		Diff:        raw,
		Coverage:    diffCoverageV1(),
	}
	if err := checkDiffEncodedBound(oversized); err == nil || !strings.Contains(strings.ToLower(err.Error()), "bound") {
		t.Fatalf("escaping-expanded diff lacked a clear bound error: %v", err)
	}
	small := candidateDiff{
		RunID:       strings.Repeat("a", 64),
		CandidateID: strings.Repeat("b", 64),
		Workspace:   "workspace",
		Diff:        "tiny",
		Coverage:    diffCoverageV1(),
	}
	if err := checkDiffEncodedBound(small); err != nil {
		t.Fatalf("small diff rejected by encoded admission: %v", err)
	}
	invalid := small
	invalid.Diff = string([]byte{0xff, 0xfe})
	if err := checkDiffEncodedBound(invalid); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("non-UTF-8 diff lacked a clear encoding error: %v", err)
	}
}

func TestRequireStableCandidateRejectsDrift(t *testing.T) {
	if err := requireStableCandidate(strings.Repeat("a", 64), strings.Repeat("a", 64)); err != nil {
		t.Fatalf("stable candidate rejected: %v", err)
	}
	if err := requireStableCandidate(strings.Repeat("a", 64), strings.Repeat("b", 64)); err == nil || !strings.Contains(err.Error(), "candidate changed") {
		t.Fatalf("drifted candidate admitted: %v", err)
	}
}

func TestDiffRejectsCandidateDriftDuringObservation(t *testing.T) {
	root, runID, workspace := diffWorkspaceFixture(t)
	flip := filepath.Join(workspace, "flip.txt")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// External editors bypass the read lease; keep the candidate moving
		// while diff observes so the drift recapture must fire.
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.WriteFile(flip, []byte(fmt.Sprintf("revision %d\n", n)), 0600)
		}
	}()
	driftRejected := false
	for i := 0; i < 30 && !driftRejected; i++ {
		var out bytes.Buffer
		err := Execute(context.Background(), []string{"diff", runID}, root, &out)
		if err == nil {
			continue
		}
		if strings.Contains(err.Error(), "candidate changed during diff observation") {
			driftRejected = true
			if out.Len() != 0 {
				t.Fatalf("drifted diff emitted partial output: %q", out.String())
			}
		}
	}
	close(stop)
	wg.Wait()
	if !driftRejected {
		t.Fatal("continuously edited workspace never triggered drift rejection")
	}
}

func TestDiffObservationPreservesIndexBytes(t *testing.T) {
	root, runID, _ := diffWorkspaceFixture(t)
	binding := mustInspectWorkspace(t, root, runID)
	// The v1 candidate identity hashes these exact raw bytes; a read-only
	// diff must leave them untouched or drift recapture misfires.
	indexPath := filepath.Join(binding.GitDir, "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Execute(context.Background(), []string{"diff", runID}, root, &out); err != nil {
		t.Fatalf("quiescent diff failed: %v", err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("read-only diff observation mutated the workspace index bytes")
	}
}

func gitUnitFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cliGit(t, dir, "init", "-q")
	cliGit(t, dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base")
	return dir
}

func TestGitOutputExitCodeOnlyForNoIndexDiff(t *testing.T) {
	dir := gitUnitFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := gitOutput(ctx, dir, false, "diff", "--no-index", "--binary", "--", "a.txt", "b.txt"); err == nil {
		t.Fatal("ordinary diff --no-index exit code 1 was silently accepted")
	}
	diff, err := gitOutput(ctx, dir, true, "diff", "--no-index", "--binary", "--", "a.txt", "b.txt")
	if err != nil || !strings.Contains(diff, "a.txt") {
		t.Fatalf("allowed --no-index difference was not returned: %q %v", diff, err)
	}
	if _, err := gitOutput(ctx, dir, false, "rev-parse", "--verify", "refs/heads/does-not-exist-anywhere"); err == nil || strings.TrimSpace(err.Error()) == "" {
		t.Fatalf("ordinary git failure was swallowed or left empty: %v", err)
	}
}

func TestGitOutputBoundedAndPreservesCancellation(t *testing.T) {
	dir := gitUnitFixture(t)
	big := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", (1<<20)+4096)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(context.Background(), dir, true, "diff", "--no-index", "--binary", "--", "/dev/null", "big.bin"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "bound") {
		t.Fatalf("oversized git output lacked a clear bound error: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gitOutput(cancelled, dir, true, "diff", "--no-index", "--binary", "--", "/dev/null", "a.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled git observation did not preserve context cancellation: %v", err)
	}
	if _, err := gitOutput(context.Background(), dir, false, "rev-parse", "--verify", "refs/heads/does-not-exist-anywhere"); err == nil {
		t.Fatal("empty-stderr git failure admitted")
	}
}

func resolveGitTestGetenv(t *testing.T, override string, strict bool) func(string) string {
	t.Helper()
	return func(key string) string {
		if key == gitExecutableEnv {
			return override
		}
		if strict {
			t.Fatalf("git resolver consulted unexpected env key %q; PATH must never be searched", key)
		}
		return ""
	}
}

func TestDefaultGitCandidatesAreFixedConventionalPaths(t *testing.T) {
	windows := defaultGitCandidates("windows")
	if len(windows) != 2 || windows[0] != "C:/Program Files/Git/cmd/git.exe" || windows[1] != "C:/Program Files/Git/bin/git.exe" {
		t.Fatalf("unexpected windows candidates: %q", windows)
	}
	linux := defaultGitCandidates("linux")
	if len(linux) != 2 || linux[0] != "/usr/bin/git" || linux[1] != "/bin/git" {
		t.Fatalf("unexpected linux candidates: %q", linux)
	}
	darwin := defaultGitCandidates("darwin")
	if len(darwin) != 3 || darwin[0] != "/usr/bin/git" || darwin[1] != "/opt/homebrew/bin/git" || darwin[2] != "/usr/local/bin/git" {
		t.Fatalf("unexpected darwin candidates: %q", darwin)
	}
	if got := defaultGitCandidates("plan9"); len(got) != 0 {
		t.Fatalf("unknown GOOS should have no conventional candidates: %q", got)
	}
}

func TestResolveGitRejectsRelativeOverrideWithoutFallback(t *testing.T) {
	fallback := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(fallback, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	getenv := resolveGitTestGetenv(t, filepath.Join("relative", "git.exe"), true)
	if _, err := resolveGitExecutable(getenv, os.Stat, filepath.EvalSymlinks, []string{fallback}); err == nil || !strings.Contains(strings.ToLower(err.Error()), "absolute") {
		t.Fatalf("relative override was not rejected as non-absolute: %v", err)
	}
}

func TestResolveGitRejectsInvalidOverrideWithoutFallback(t *testing.T) {
	fallback := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(fallback, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cases := map[string]string{
		"missing absolute":    filepath.Join(t.TempDir(), "does-not-exist-git"),
		"directory override":  dir,
		"nonregular override": dir,
	}
	for name, override := range cases {
		getenv := resolveGitTestGetenv(t, override, true)
		if _, err := resolveGitExecutable(getenv, os.Stat, filepath.EvalSymlinks, []string{fallback}); err == nil {
			t.Fatalf("%s override silently fell back to conventional candidates", name)
		}
	}
	// A relative override alongside a valid fallback must also fail, never
	// silently fall back.
	getenv := resolveGitTestGetenv(t, "git", true)
	if _, err := resolveGitExecutable(getenv, os.Stat, filepath.EvalSymlinks, []string{fallback}); err == nil {
		t.Fatal("bare-command override silently fell back to conventional candidates")
	}
}

func TestResolveGitAcceptsValidAbsoluteOverride(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "custom-git")
	if err := os.WriteFile(explicit, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(explicit)
	if err != nil {
		t.Fatal(err)
	}
	getenv := resolveGitTestGetenv(t, explicit, true)
	got, err := resolveGitExecutable(getenv, os.Stat, filepath.EvalSymlinks, []string{filepath.Join(dir, "does-not-exist")})
	if err != nil {
		t.Fatalf("valid absolute override rejected: %v", err)
	}
	if got != want {
		t.Fatalf("valid override resolved to %q, want %q", got, want)
	}
}

func TestResolveGitOverrideResolvesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real-git")
	if err := os.WriteFile(target, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link-git")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	want, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(want) {
		t.Fatalf("symlink resolution did not stay absolute: %q", want)
	}
	getenv := resolveGitTestGetenv(t, link, true)
	got, err := resolveGitExecutable(getenv, os.Stat, filepath.EvalSymlinks, nil)
	if err != nil {
		t.Fatalf("symlinked override rejected: %v", err)
	}
	if got != want {
		t.Fatalf("symlinked override resolved to %q, want %q", got, want)
	}
}

func TestResolveGitDefaultNeverConsultsHostilePATH(t *testing.T) {
	dir := t.TempDir()
	systemGit := filepath.Join(dir, "system-git")
	if err := os.WriteFile(systemGit, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(systemGit)
	if err != nil {
		t.Fatal(err)
	}
	// The resolver only ever reads FABRIC_GIT_EXECUTABLE; any other env
	// lookup (notably PATH) fails the test.
	getenv := resolveGitTestGetenv(t, "", true)
	got, err := resolveGitExecutable(getenv, os.Stat, filepath.EvalSymlinks, []string{systemGit})
	if err != nil {
		t.Fatalf("conventional fallback rejected: %v", err)
	}
	if got != want {
		t.Fatalf("default resolved to %q, want conventional %q", got, want)
	}
	// A hostile PATH value must be ignored even when it names an attacker
	// executable: only the injected conventional candidates may be selected.
	evilDir := t.TempDir()
	evilGit := filepath.Join(evilDir, "git")
	if err := os.WriteFile(evilGit, []byte("evil"), 0600); err != nil {
		t.Fatal(err)
	}
	hostile := func(key string) string {
		switch key {
		case gitExecutableEnv:
			return ""
		case "PATH":
			return evilDir + string(os.PathListSeparator) + os.Getenv("PATH")
		default:
			return ""
		}
	}
	got, err = resolveGitExecutable(hostile, os.Stat, filepath.EvalSymlinks, []string{systemGit})
	if err != nil {
		t.Fatalf("hostile PATH broke conventional fallback: %v", err)
	}
	if got != want {
		t.Fatalf("hostile PATH diverted resolution to %q, want %q", got, want)
	}
	if got == evilGit {
		t.Fatalf("resolution selected PATH executable %q", got)
	}
}

func TestResolveGitSkipsMissingCandidates(t *testing.T) {
	dir := t.TempDir()
	second := filepath.Join(dir, "second-git")
	if err := os.WriteFile(second, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(second)
	if err != nil {
		t.Fatal(err)
	}
	getenv := resolveGitTestGetenv(t, "", true)
	got, err := resolveGitExecutable(getenv, os.Stat, filepath.EvalSymlinks, []string{filepath.Join(dir, "missing-git"), dir, second})
	if err != nil {
		t.Fatalf("valid fallback skipped: %v", err)
	}
	if got != want {
		t.Fatalf("fallback resolved to %q, want %q", got, want)
	}
	if _, err := resolveGitExecutable(getenv, os.Stat, filepath.EvalSymlinks, nil); err == nil {
		t.Fatal("empty conventional candidates admitted")
	}
}
