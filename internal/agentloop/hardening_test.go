package agentloop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/cost"
	"github.com/iancuuandrei/fabric/internal/sandbox"
)

func TestHTTPModelRetriesTransient429AndFailsFastOnQuota(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After-Ms", "10")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate_limit_exceeded"}`))
			return
		}
		if n == 2 {
			w.Header().Set("Retry-After-Ms", "10")
			w.WriteHeader(529)
			_, _ = w.Write([]byte(`{"error":"overloaded"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stop_reason":"end_turn","content":[{"type":"text","text":"recovered"}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer srv.Close()

	m := &HTTPModel{
		Dialect:     "anthropic",
		BaseURL:     srv.URL,
		ModelName:   "claude-sonnet-4-5",
		MaxAttempts: 4,
		Client:      srv.Client(),
	}
	resp, err := m.Complete(context.Background(), "sys", []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("expected recovery after transient 429/529, got: %v", err)
	}
	if resp.Text != "recovered" || calls.Load() != 3 {
		t.Fatalf("unexpected response=%+v calls=%d", resp, calls.Load())
	}

	// Hard quota exhaustion must NOT retry.
	var quotaCalls atomic.Int32
	qSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		quotaCalls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"insufficient_quota"}}`))
	}))
	defer qSrv.Close()

	qm := &HTTPModel{Dialect: "openai", BaseURL: qSrv.URL, ModelName: "gpt-5", MaxAttempts: 4, Client: qSrv.Client()}
	if _, err := qm.Complete(context.Background(), "sys", []Message{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Fatal("expected error on insufficient_quota")
	}
	if quotaCalls.Load() != 1 {
		t.Fatalf("expected fail-fast (1 call) on insufficient_quota, got %d", quotaCalls.Load())
	}
}

func TestFinalTurnReviewRejectionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit("init", "-q")
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main(){}\n"), 0o600)
	runGit("add", ".")
	runGit("-c", "user.name=T", "-c", "user.email=t@e.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "init")

	worker := &stubFinalModel{name: "claude-sonnet-4-5", text: "done"}
	reviewer := &stubFinalModel{name: "claude-sonnet-4-5", text: `{"verdict":"changes_requested","notes":"missing nil check"}`}

	ep, err := Run(context.Background(), Config{
		RepoRoot:      dir,
		Objective:     "Fix bug",
		Model:         worker,
		ReviewerModel: reviewer,
		MaxTurns:      1, // final turn!
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if ep.Status == "READY" {
		t.Fatalf("expected fail-closed non-READY status on final-turn changes_requested, got READY")
	}
	if ep.ReviewVerdict != "changes_requested" {
		t.Fatalf("expected review_verdict=changes_requested, got %q", ep.ReviewVerdict)
	}
}

func TestUntrackedFilesBinarySearchAndGraphTools(t *testing.T) {
	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit("init", "-q")
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/t\n\ngo 1.24\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "core.go"), []byte("package t\nfunc AlphaHelper() int { return 1 }\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "bin.dat"), []byte{0x00, 0x01, 'f', 'o', 'o', 0x00}, 0o600)
	runGit("add", ".")
	runGit("-c", "user.name=T", "-c", "user.email=t@e.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "init")

	wt, err := NewWorkspaceTools(dir, nil, sandbox.Config{}, false)
	if err != nil {
		t.Fatal(err)
	}

	// Write a brand-new untracked file and verify git_diff sees it.
	res := wt.Execute(context.Background(), ToolCall{
		ID:        "w1",
		Name:      "write_file",
		Arguments: json.RawMessage(`{"path":"caller.go","content":"package t\nfunc Use() int { return AlphaHelper() }\n"}`),
	})
	if res.IsError {
		t.Fatalf("write_file failed: %s", res.Output)
	}

	diffRes := wt.Execute(context.Background(), ToolCall{ID: "d1", Name: "git_diff", Arguments: json.RawMessage(`{}`)})
	if diffRes.IsError || !strings.Contains(diffRes.Output, "caller.go") {
		t.Fatalf("expected untracked caller.go in git_diff, got: %s", diffRes.Output)
	}

	// search_text must skip bin.dat even though it contains "foo".
	sr := wt.Execute(context.Background(), ToolCall{ID: "s1", Name: "search_text", Arguments: json.RawMessage(`{"query":"foo"}`)})
	if sr.IsError || strings.Contains(sr.Output, "bin.dat") {
		t.Fatalf("expected binary file skipped by search_text, got: %s", sr.Output)
	}

	// Graph tools: repo_map and graph_impact.
	rm := wt.Execute(context.Background(), ToolCall{ID: "g1", Name: "repo_map", Arguments: json.RawMessage(`{}`)})
	if rm.IsError || !strings.Contains(rm.Output, "core.go") {
		t.Fatalf("unexpected repo_map output: %s", rm.Output)
	}
	imp := wt.Execute(context.Background(), ToolCall{ID: "g2", Name: "graph_impact", Arguments: json.RawMessage(`{"target":"AlphaHelper","depth":2}`)})
	if imp.IsError || !strings.Contains(imp.Output, "caller.go") {
		t.Fatalf("expected caller.go in graph_impact for AlphaHelper, got: %s", imp.Output)
	}
	cc := wt.Execute(context.Background(), ToolCall{ID: "g3", Name: "compile_context", Arguments: json.RawMessage(`{"query":"AlphaHelper","budget_tokens":1500,"spec_only":true}`)})
	if cc.IsError || !strings.Contains(cc.Output, "AlphaHelper") {
		t.Fatalf("expected AlphaHelper in compile_context output, got: err=%v out=%s", cc.IsError, cc.Output)
	}

	// delete_file safely deletes caller.go.
	del := wt.Execute(context.Background(), ToolCall{ID: "x1", Name: "delete_file", Arguments: json.RawMessage(`{"path":"caller.go"}`)})
	if del.IsError {
		t.Fatalf("delete_file failed: %s", del.Output)
	}

	// Inline diagnostics triggered immediately on malformed Go write.
	badRes := wt.Execute(context.Background(), ToolCall{
		ID:        "w2",
		Name:      "write_file",
		Arguments: json.RawMessage(`{"path":"broken.go","content":"package t\nfunc Bad( {\n"}`),
	})
	if badRes.IsError || !strings.Contains(badRes.Output, `"diagnostics"`) {
		t.Fatalf("expected inline diagnostics on broken Go file, got: %s", badRes.Output)
	}
	_ = wt.Execute(context.Background(), ToolCall{ID: "x2", Name: "delete_file", Arguments: json.RawMessage(`{"path":"broken.go"}`)})

	// CapScope read-only capability enforcement blocks writes cleanly.
	wt.Capabilities = ReadOnlyCapabilities()
	denied := wt.Execute(context.Background(), ToolCall{
		ID:        "w3",
		Name:      "write_file",
		Arguments: json.RawMessage(`{"path":"nope.go","content":"package t\n"}`),
	})
	if !denied.IsError || !strings.Contains(denied.Output, "capability denied") {
		t.Fatalf("expected CapScope capability denial, got: %s", denied.Output)
	}
}

func TestEpisodeResumeWithHigherBudgetAndLiveSteering(t *testing.T) {
	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit("init", "-q")
	_ = os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\nconst V = 1\n"), 0o600)
	_ = os.MkdirAll(filepath.Join(dir, ".agents", "skills", "demo-skill"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, ".agents", "skills", "demo-skill", "SKILL.md"), []byte("---\nname: demo-skill\ndescription: Demo skill\n---\nAlways check V.\n"), 0o600)
	runGit("add", ".")
	runGit("-c", "user.name=T", "-c", "user.email=t@e.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "init")

	mdl := &seqModel{
		name: "claude-sonnet-4-5",
		responses: []ModelResponse{
			{ToolCalls: []ToolCall{{ID: "s1", Name: "list_skills", Arguments: json.RawMessage(`{}`)}}, Usage: cost.Usage{InputTokens: 100, OutputTokens: 20}},
			{ToolCalls: []ToolCall{{ID: "s2", Name: "load_skill", Arguments: json.RawMessage(`{"name":"demo-skill"}`)}}, Usage: cost.Usage{InputTokens: 100, OutputTokens: 20}},
			{Text: "done after resume and steering", Usage: cost.Usage{InputTokens: 80, OutputTokens: 15}},
		},
	}

	// Phase 1: Stop after Turn 1 due to max_turns=1 budget cap.
	ep1, err := Run(context.Background(), Config{
		RepoRoot:  dir,
		Objective: "Inspect demo skill then finish",
		Model:     mdl,
		MaxTurns:  1,
	})
	if err != nil {
		t.Fatalf("initial Run error: %v", err)
	}
	if ep1.Status != "NEEDS_ATTENTION" || ep1.Turns != 1 {
		t.Fatalf("expected NEEDS_ATTENTION after turn 1, got status=%s turns=%d reason=%s", ep1.Status, ep1.Turns, ep1.Reason)
	}

	// Phase 2: Resume with higher turn budget and live steering message.
	steerCh := make(chan string, 2)
	steerCh <- "Prioritize concise summary."
	ep2, err := Run(context.Background(), Config{
		RepoRoot:    dir,
		ResumeRunID: ep1.RunID,
		Model:       mdl,
		MaxTurns:    5,
		SteerCh:     steerCh,
	})
	if err != nil {
		t.Fatalf("resume Run error: %v", err)
	}
	if ep2.Status != "READY" || ep2.Turns < 3 {
		t.Fatalf("expected READY on resumed episode, got status=%s turns=%d reason=%s", ep2.Status, ep2.Turns, ep2.Reason)
	}
	sawSteered := false
	for _, ev := range ep2.Events {
		if ev.Kind == "steered" {
			sawSteered = true
		}
	}
	if !sawSteered {
		t.Fatalf("expected steered event in resumed episode, got events: %+v", ep2.Events)
	}
}

type seqModel struct {
	name      string
	responses []ModelResponse
	idx       int
}

func (m *seqModel) Name() string { return m.name }
func (m *seqModel) Complete(_ context.Context, _ string, _ []Message, _ []ToolDef) (ModelResponse, error) {
	if m.idx >= len(m.responses) {
		return ModelResponse{Text: "done"}, nil
	}
	r := m.responses[m.idx]
	m.idx++
	return r, nil
}

type stubFinalModel struct {
	name string
	text string
}

func (m *stubFinalModel) Name() string { return m.name }
func (m *stubFinalModel) Complete(_ context.Context, _ string, _ []Message, _ []ToolDef) (ModelResponse, error) {
	return ModelResponse{Text: m.text, Usage: cost.Usage{InputTokens: 50, OutputTokens: 20}}, nil
}

func TestDiscoverRepositorySkills(t *testing.T) {
	skills := discoverSkills(filepath.Join("..", "..", ".agents", "skills"))
	if len(skills) < 32 {
		t.Fatalf("expected at least 32 discovered skills, got %d", len(skills))
	}
	required := []string{
		"code-hygiene",
		"concurrency-resource-safety",
		"safe-migrations-and-contracts",
		"ci-and-supply-chain-hygiene",
		"verification-before-completion",
		"performance-and-profiling",
		"modular-design-and-boundaries",
		"clean-code-and-control-flow",
		"error-handling-and-observability",
		"api-and-interface-ergonomics",
		"brownfield-convention-matching",
		"repo-onboarding-and-integration",
	}
	byName := make(map[string]SkillMetadata, len(skills))
	for _, s := range skills {
		if s.Description == "" || strings.HasPrefix(s.Description, "Engineering workflow skill: ") {
			t.Fatalf("skill %s missing custom single-line description in frontmatter", s.Name)
		}
		body, ok := loadSkillBody(skills, s.Name)
		if !ok || strings.TrimSpace(body) == "" {
			t.Fatalf("loadSkillBody failed for %s", s.Name)
		}
		byName[s.Name] = s
	}
	for _, name := range required {
		if _, ok := byName[name]; !ok {
			t.Fatalf("expected skill %q in repository catalog", name)
		}
	}
}

func TestEmbeddedSkillsFallbackAndInstall(t *testing.T) {
	emptyRepo := t.TempDir()
	skills := DiscoverSkills(emptyRepo)
	if len(skills) < 32 {
		t.Fatalf("expected >= 32 embedded fallback skills in empty repo, got %d", len(skills))
	}
	for _, s := range skills {
		if s.Source != "built-in" {
			t.Fatalf("expected built-in source in empty repo, got %+v", s)
		}
	}
	if body, ok := LoadSkillBody(skills, "modular-design-and-boundaries"); !ok || !strings.Contains(body, "Deep Modules") {
		t.Fatalf("failed loading embedded body for modular-design-and-boundaries: ok=%v", ok)
	}

	// Install selected built-ins cleanly, verify repo-local override, and verify overwrite/negative guards.
	installed, skipped, err := InstallBuiltinSkills(emptyRepo, []string{"code-hygiene", "brownfield-convention-matching"}, false)
	if err != nil || len(installed) != 2 || len(skipped) != 0 {
		t.Fatalf("unexpected first InstallBuiltinSkills result: installed=%v skipped=%v err=%v", installed, skipped, err)
	}
	installed2, skipped2, err := InstallBuiltinSkills(emptyRepo, []string{"code-hygiene"}, false)
	if err != nil || len(installed2) != 0 || len(skipped2) != 1 {
		t.Fatalf("expected existing skill skipped without overwrite, got installed=%v skipped=%v err=%v", installed2, skipped2, err)
	}
	overridePath := filepath.Join(emptyRepo, ".agents", "skills", "code-hygiene", "SKILL.md")
	_ = os.WriteFile(overridePath, []byte("---\nname: code-hygiene\ndescription: Custom team hygiene rule.\n---\nCustom body\n"), 0o644)
	overlaid := DiscoverSkills(emptyRepo)
	var foundOverride *SkillMetadata
	for i := range overlaid {
		if overlaid[i].Name == "code-hygiene" {
			foundOverride = &overlaid[i]
			break
		}
	}
	if foundOverride == nil || foundOverride.Source != "repo" || foundOverride.Description != "Custom team hygiene rule." {
		t.Fatalf("expected repo override for code-hygiene, got %+v", foundOverride)
	}
	if _, _, err := InstallBuiltinSkills(emptyRepo, []string{"../evil"}, false); err == nil {
		t.Fatal("expected traversal skill name rejected")
	}
	if _, _, err := InstallBuiltinSkills(emptyRepo, []string{"nonexistent-skill-xyz"}, false); err == nil {
		t.Fatal("expected unknown skill name rejected")
	}
}

func TestWorkflowNativeSkillsRepairAndAutoReview(t *testing.T) {
	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit("init", "-q")
	_ = os.WriteFile(filepath.Join(dir, "service.go"), []byte("package svc\nfunc Guard() bool { return true }\n"), 0o600)
	runGit("add", ".")
	runGit("-c", "user.name=T", "-c", "user.email=t@e.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "init")

	mdl := &seqModel{
		name: "claude-sonnet-4-5",
		responses: []ModelResponse{
			// Turn 1: modify service.go without PASSED_GATE -> triggers verify fail & repair skill.
			{ToolCalls: []ToolCall{{
				ID:        "e1",
				Name:      "write_file",
				Arguments: json.RawMessage(`{"path":"service.go","content":"package svc\nimport \"sync\"\nvar mu sync.Mutex\nfunc Guard() bool { mu.Lock(); defer mu.Unlock(); return true }\n"}`),
			}}, Usage: cost.Usage{InputTokens: 120, OutputTokens: 30}},
			// Turn 2: finish -> fails gate check.
			{Text: "added mutex guard", Usage: cost.Usage{InputTokens: 100, OutputTokens: 20}},
			// Turn 3: repair after failed gate -> add PASSED_GATE marker.
			{ToolCalls: []ToolCall{{
				ID:        "e2",
				Name:      "edit_file",
				Arguments: json.RawMessage(`{"path":"service.go","old_string":"return true","new_string":"// PASSED_GATE\n\treturn true"}`),
			}}, Usage: cost.Usage{InputTokens: 120, OutputTokens: 30}},
			// Turn 4: finish -> passes gate -> AutoReview #1 runs.
			{Text: "gate fixed", Usage: cost.Usage{InputTokens: 100, OutputTokens: 20}},
			// AutoReview #1 (using same model): requests test coverage.
			{Text: `{"verdict":"changes_requested","notes":"missing unit test for Guard"}`, Usage: cost.Usage{InputTokens: 150, OutputTokens: 25}},
			// Turn 5: add unit test file.
			{ToolCalls: []ToolCall{{
				ID:        "w3",
				Name:      "write_file",
				Arguments: json.RawMessage(`{"path":"service_test.go","content":"package svc\nimport \"testing\"\nfunc TestGuard(t *testing.T) { if !Guard() { t.Fatal(\"fail\") } }\n"}`),
			}}, Usage: cost.Usage{InputTokens: 120, OutputTokens: 30}},
			// Turn 6: finish -> passes gate -> AutoReview #2 runs.
			{Text: "added unit test", Usage: cost.Usage{InputTokens: 100, OutputTokens: 20}},
			// AutoReview #2: approves.
			{Text: `{"verdict":"approve","notes":"clean mutex guard and test coverage"}`, Usage: cost.Usage{InputTokens: 150, OutputTokens: 25}},
		},
	}

	ep, err := Run(context.Background(), Config{
		RepoRoot:     dir,
		Objective:    "Fix race condition in Guard with mutex and tests",
		Model:        mdl,
		AutoReview:   true,
		MaxTurns:     10,
		Verification: []config.Check{{Name: "gate", Argv: []string{"git", "grep", "-q", "PASSED_GATE", "service.go"}, TimeoutSeconds: 10}},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if ep.Status != "READY" || ep.ReviewVerdict != "approve" {
		t.Fatalf("expected READY + approve, got status=%s verdict=%q reason=%s", ep.Status, ep.ReviewVerdict, ep.Reason)
	}

	stages := map[string]bool{}
	skills := map[string]bool{}
	for _, act := range ep.SkillsUsed {
		stages[act.Stage] = true
		skills[act.Skill] = true
	}
	for _, wantStage := range []string{"discover", "repair", "verify", "review"} {
		if !stages[wantStage] {
			t.Fatalf("expected stage %q in SkillsUsed, got %+v", wantStage, ep.SkillsUsed)
		}
	}
	for _, wantSkill := range []string{"repair", "verification-before-completion", "code-review", "code-hygiene", "concurrency-resource-safety"} {
		if !skills[wantSkill] {
			t.Fatalf("expected skill %q activated natively, got %+v", wantSkill, ep.SkillsUsed)
		}
	}
}
