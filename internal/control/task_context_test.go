package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
	"github.com/iancuuandrei/fabric/internal/verification"
)

func boundedTaskCreation(t *testing.T, maxRepairs int) Creation {
	t.Helper()
	c := creation(t)
	c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-writer", Effort: "high", Role: "writer"}
	c.Config.Explorer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-explorer", Effort: "low", Role: "explorer"}
	c.Config.Reviewer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-reviewer", Effort: "high", Role: "reviewer"}
	c.Execution = &ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: maxRepairs, Context: taskContextBoundedV1}
	return c
}

func boundedTaskFixture(t *testing.T, files map[string]string) (string, Snapshot) {
	t.Helper()
	return boundedTaskFixtureWithCreation(t, boundedTaskCreation(t, 2), files)
}

func boundedTaskFixtureWithCreation(t *testing.T, c Creation, files map[string]string) (string, Snapshot) {
	t.Helper()
	root := c.Repository.Root
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(b))
		}
	}
	git("init", "-q")
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		git("add", name)
	}
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	var err error
	c.Repository, err = repository.Discover(context.Background(), root, c.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(p, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(p, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	var inv runtime.Invocation
	if c.Execution != nil && c.Execution.PromptRecipe != "" {
		inv, err = plannerInvocationWithContextAndRecipe(c.Config, c.Objective, nil, c.Execution)
	} else {
		inv, err = plannerInvocation(c.Config, c.Objective)
	}
	if err != nil {
		t.Fatal(err)
	}
	f := &runtime.Fake{}
	result, err := f.Execute(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	if err = Append(p, "plan.recorded", result); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = Append(p, "plan.approved", Approval{s.PlanID, "operator"}); err != nil {
		t.Fatal(err)
	}
	s, err = StartWorkspace(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return p, s
}

func TestTaskContextAdmissionOmitsProtectedWorkflowWithPromptRecipe(t *testing.T) {
	c := boundedTaskCreation(t, 2)
	c.Execution.PromptRecipe = promptRecipeCachePrefixV1
	p, s := boundedTaskFixtureWithCreation(t, c, map[string]string{
		".github/workflows/ci.yml": "name: ci\n",
		"internal/example.go":      "package example\n\nfunc Value() int { return 7 }\n",
	})

	record, err := AdmitTaskContext(context.Background(), p, "writer", s.Creation.Objective)
	if err != nil {
		t.Fatalf("bounded writer context admission failed: %v", err)
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	if record.Role != "writer" || record.CandidateID != candidateID || record.ManifestID == "" || record.QueryHash != taskContextQueryHash(s.Creation.Objective) {
		t.Fatalf("writer context binding is incomplete: %#v", record)
	}
	foundGo := false
	foundWorkflowOmission := false
	for _, selected := range record.Manifest.Selected {
		if selected.Path == "internal/example.go" && strings.Contains(selected.Content, "func Value") {
			foundGo = true
		}
		if selected.Path == ".github/workflows/ci.yml" {
			t.Fatal("protected workflow content was selected")
		}
	}
	for _, omission := range record.Manifest.Omissions {
		if omission.Path == ".github/workflows/ci.yml" && omission.Reason == "unreadable" {
			foundWorkflowOmission = true
		}
	}
	if !foundGo || !foundWorkflowOmission {
		t.Fatalf("expected eligible Go source and explicit workflow omission: selected=%#v omissions=%#v", record.Manifest.Selected, record.Manifest.Omissions)
	}

	invocation, err := PrepareWriterInvocation(p)
	if err != nil {
		t.Fatalf("writer invocation did not reuse admitted context: %v", err)
	}
	input := taskContextInputOf(t, invocation)
	encoded, err := json.Marshal(input["task_context"])
	if err != nil {
		t.Fatal(err)
	}
	var bound TaskContextRecord
	if err := canonical.Decode(encoded, &bound); err != nil {
		t.Fatalf("writer task context is not canonical: %v", err)
	}
	if bound.QueryHash != record.QueryHash || bound.ManifestID != record.ManifestID {
		t.Fatal("cache-prefix writer invocation did not preserve its exact query and manifest binding")
	}
}

func taskContextInputOf(t *testing.T, inv runtime.Invocation) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(inv.Input), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestTaskContextStoresSelectedTextAndPromptBindsManifest(t *testing.T) {
	p, s := boundedTaskFixture(t, map[string]string{"file.txt": "base\n", "docs/guide.txt": "greeting trimming objective and regression notes\n"})
	question := "Explain file.txt base and greeting trimming"
	rec, err := AdmitTaskContext(context.Background(), p, "explorer", question)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Role != "explorer" || rec.ManifestID == "" {
		t.Fatalf("record missing role/manifest: %+v", rec)
	}
	gotID, err := rec.Manifest.ID()
	if err != nil || gotID != rec.ManifestID {
		t.Fatalf("manifest identity not bound: %v %s", err, rec.ManifestID)
	}
	sourceID, _ := s.Creation.Repository.ID()
	candidateID, _ := s.Candidate.ID()
	if rec.SourceID != sourceID || rec.CandidateID != candidateID {
		t.Fatal("source/candidate binding mismatch")
	}
	sum := sha256.Sum256([]byte(question))
	if rec.QueryHash != hex.EncodeToString(sum[:]) || rec.Query != question || rec.QueryTruncated {
		t.Fatal("query binding mismatch")
	}
	if len(rec.Manifest.Selected) == 0 {
		t.Fatal("no source text selected")
	}
	found := false
	for _, sel := range rec.Manifest.Selected {
		if err := safepath.Relative(sel.Path); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(sel.Content))
		if hex.EncodeToString(sum[:]) != sel.ExcerptHash || int64(len(sel.Content)) != sel.End-sel.Start {
			t.Fatal("excerpt hash/range mismatch")
		}
		if strings.Contains(sel.Content, "base") {
			found = true
		}
	}
	if !found {
		t.Fatalf("selected source text missing candidate bytes: %+v", rec.Manifest.Selected)
	}
	inv, err := PrepareExplorerInvocation(p, question)
	if err != nil {
		t.Fatal(err)
	}
	m := taskContextInputOf(t, inv)
	tc, ok := m["task_context"]
	if !ok {
		t.Fatal("explorer prompt lacks persisted task_context")
	}
	raw, _ := json.Marshal(tc)
	var decoded TaskContextRecord
	if err := canonical.Decode(raw, &decoded); err != nil {
		t.Fatalf("persisted manifest not canonical: %v", err)
	}
	if decoded.ManifestID != rec.ManifestID || len(decoded.Manifest.Selected) != len(rec.Manifest.Selected) {
		t.Fatal("prompt manifest differs from persisted record")
	}
	wrec, err := AdmitTaskContext(context.Background(), p, "writer", s.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	winv, err := PrepareWriterInvocation(p)
	if err != nil {
		t.Fatal(err)
	}
	wm := taskContextInputOf(t, winv)
	wtc, ok := wm["task_context"]
	if !ok {
		t.Fatal("writer prompt lacks task_context")
	}
	wraw, _ := json.Marshal(wtc)
	var wdecoded TaskContextRecord
	if err := canonical.Decode(wraw, &wdecoded); err != nil {
		t.Fatalf("writer manifest not canonical: %v", err)
	}
	if wrec.Role != "writer" || wrec.CandidateID != candidateID || wrec.ManifestID == "" {
		t.Fatal("writer record binding mismatch")
	}
	if wdecoded.ManifestID != wrec.ManifestID {
		t.Fatal("writer prompt manifest differs from persisted record")
	}
}

func TestTaskContextReplayIgnoresMutableEdits(t *testing.T) {
	p, s := boundedTaskFixture(t, map[string]string{"file.txt": "base\n"})
	question := "Explain file.txt base"
	rec, err := AdmitTaskContext(context.Background(), p, "explorer", question)
	if err != nil {
		t.Fatal(err)
	}
	before, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Workspace.Request.Path, "file.txt"), []byte("mutated without admission\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := Inspect(p)
	if err != nil {
		t.Fatal(err, "replay read the filesystem")
	}
	if len(after.TaskContexts) != len(before.TaskContexts) || after.TaskContexts[0].ManifestID != rec.ManifestID {
		t.Fatal("replay changed persisted context after mutable edit")
	}
	inv, err := PrepareExplorerInvocation(p, question)
	if err != nil {
		t.Fatal(err)
	}
	m := taskContextInputOf(t, inv)
	tc, ok := m["task_context"]
	if !ok {
		t.Fatal("prompt lost persisted task_context after mutable edit")
	}
	raw, _ := json.Marshal(tc)
	var decoded TaskContextRecord
	if err := canonical.Decode(raw, &decoded); err != nil {
		t.Fatalf("persisted task_context not canonical after replay: %v", err)
	}
	if decoded.ManifestID != rec.ManifestID {
		t.Fatal("replay prompt manifest differs from persisted record")
	}
	if len(decoded.Manifest.Selected) != len(rec.Manifest.Selected) {
		t.Fatalf("replay prompt selection count changed: %d != %d", len(decoded.Manifest.Selected), len(rec.Manifest.Selected))
	}
	for i := range rec.Manifest.Selected {
		want := rec.Manifest.Selected[i]
		got := decoded.Manifest.Selected[i]
		if got.Path != want.Path || got.Content != want.Content || got.ExcerptHash != want.ExcerptHash || got.Start != want.Start || got.End != want.End {
			t.Fatalf("replay prompt selected content mismatch at %d: %+v != %+v", i, got, want)
		}
		if strings.Contains(got.Content, "mutated without admission") {
			t.Fatal("prompt used mutable source instead of persisted manifest")
		}
	}
	if err := os.WriteFile(filepath.Join(s.Workspace.Request.Path, "file.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestTaskContextRejectsSubstitutionForgeryDuplicate(t *testing.T) {
	p, _ := boundedTaskFixture(t, map[string]string{"file.txt": "base\n"})
	question := "Explain file.txt base"
	rec, err := AdmitTaskContext(context.Background(), p, "explorer", question)
	if err != nil {
		t.Fatal(err)
	}
	sub := rec
	sub.CandidateID = strings.Repeat("0", 64)
	if err := Append(p, "task.context-admitted", sub); err == nil {
		t.Fatal("candidate substitution admitted")
	}
	forged := rec
	forged.Manifest.Selected[0].Content += " forged"
	if err := Append(p, "task.context-admitted", forged); err == nil {
		t.Fatal("forged excerpt admitted")
	}
	if err := Append(p, "task.context-admitted", rec); err == nil {
		t.Fatal("duplicate record admitted")
	}
	other := rec
	other.Manifest.Selected[0].Reason = "changed_path"
	id, err := other.Manifest.ID()
	if err != nil {
		t.Fatal(err)
	}
	other.ManifestID = id
	if err := Append(p, "task.context-admitted", other); err == nil {
		t.Fatal("substituted second admission for same query admitted")
	}
	events, err := journal.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range events {
		if e.Kind == "task.context-admitted" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("rejected records changed the journal: %d", n)
	}
}

func TestTaskContextSameManifestCanBeAdmittedForDifferentRoles(t *testing.T) {
	p, s := boundedTaskFixture(t, map[string]string{"file.txt": "base\n"})
	question := s.Creation.Objective
	writer, err := AdmitTaskContext(context.Background(), p, "writer", question)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := AdmitTaskContext(context.Background(), p, "reviewer", question)
	if err != nil {
		t.Fatalf("same candidate manifest was rejected for reviewer role: %v", err)
	}
	if writer.ManifestID == "" || reviewer.ManifestID != writer.ManifestID {
		t.Fatalf("role-specific admission unexpectedly changed content manifest: writer=%q reviewer=%q", writer.ManifestID, reviewer.ManifestID)
	}
	got, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TaskContexts) != 2 || got.TaskContexts[0].Role != "writer" || got.TaskContexts[1].Role != "reviewer" {
		t.Fatalf("role-bound records did not replay independently: %+v", got.TaskContexts)
	}
	if _, err := AdmitTaskContext(context.Background(), p, "writer", question); err != nil {
		t.Fatalf("same-role idempotent admission failed: %v", err)
	}
	if err := Append(p, "task.context-admitted", writer); err == nil {
		t.Fatal("same-role duplicate record was admitted")
	}
}

func TestTaskContextLegacyInvocationByteIdentical(t *testing.T) {
	c := creation(t)
	c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-writer", Effort: "high", Role: "writer"}
	c.Config.Explorer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-explorer", Effort: "low", Role: "explorer"}
	p, _ := approvedRepositoryCreation(t, c)
	if _, err := StartWorkspace(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	explorerInv, err := PrepareExplorerInvocation(p, "Explain file.txt base")
	if err != nil {
		t.Fatal(err)
	}
	writerInv, err := PrepareWriterInvocation(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, inv := range []runtime.Invocation{explorerInv, writerInv} {
		if strings.Contains(inv.Input, "task_context") {
			t.Fatal("legacy invocation carries task_context")
		}
	}
	before, err := journal.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitTaskContext(context.Background(), p, "explorer", "Explain file.txt base"); err == nil {
		t.Fatal("legacy run admitted task context")
	}
	if err := maybeAdmitTaskContext(context.Background(), p, "explorer", "Explain file.txt base"); err != nil {
		t.Fatal(err)
	}
	after, err := journal.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatal("legacy context admission changed the journal")
	}
	raw, err := canonical.Bytes(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "context") {
		t.Fatal("legacy creation changed canonical identity")
	}
}

func TestTaskContextExcludesSensitiveAndLargeFiles(t *testing.T) {
	large := strings.Repeat("x", 40<<10) + " greeting target\n"
	p, _ := boundedTaskFixture(t, map[string]string{
		"file.txt":           "base greeting target\n",
		".env":               "API_KEY=live-secret\n",
		"secrets/deploy.txt": "token material\n",
		"large.bin":          large,
		"docs/guide.txt":     "ordinary greeting notes\n",
		"notes/secretary.md": "meeting notes about greeting\n",
	})
	if !taskcontext.EligiblePath("file.txt") || !taskcontext.EligiblePath("docs/guide.txt") || !taskcontext.EligiblePath("notes/secretary.md") {
		t.Fatal("ordinary path marked ineligible")
	}
	if taskcontext.EligiblePath(".env") || taskcontext.EligiblePath("secrets/deploy.txt") || taskcontext.EligiblePath("config/.env.local") {
		t.Fatal("sensitive path marked eligible")
	}
	rec, err := AdmitTaskContext(context.Background(), p, "explorer", "greeting target objective")
	if err != nil {
		t.Fatal(err)
	}
	for _, sel := range rec.Manifest.Selected {
		if strings.Contains(sel.Content, "API_KEY") || strings.Contains(sel.Content, "live-secret") {
			t.Fatal("sensitive content selected")
		}
		if sel.Path == ".env" || sel.Path == "secrets/deploy.txt" || sel.Path == "large.bin" {
			t.Fatalf("excluded file selected: %s", sel.Path)
		}
	}
	for _, o := range rec.Manifest.Omissions {
		if strings.Contains(o.Path, "API_KEY") || strings.Contains(o.Path, "live-secret") {
			t.Fatal("sensitive path leaked in omission")
		}
	}
	foundSensitive := false
	foundLarge := false
	for _, o := range rec.Manifest.Omissions {
		if o.Path == "[redacted]" && o.Reason == "sensitive_path" {
			foundSensitive = true
		}
		if o.Path == "large.bin" && o.Reason == "file_too_large" {
			foundLarge = true
		}
	}
	if !foundSensitive {
		t.Fatalf("sensitive omission missing: %+v", rec.Manifest.Omissions)
	}
	if !foundLarge {
		t.Fatalf("large-file omission missing: %+v", rec.Manifest.Omissions)
	}
	if rec.Manifest.SelectedBytes > 48<<10 || len(rec.Manifest.Selected) > 12 {
		t.Fatal("selection exceeded default 48KiB/12-file budget")
	}
}

func TestTaskContextRepairRequiresNewCandidate(t *testing.T) {
	p, s := boundedTaskFixture(t, map[string]string{"file.txt": "base\n"})
	first, err := AdmitTaskContext(context.Background(), p, "writer", s.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	eventsBefore, err := journal.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	second, err := AdmitTaskContext(context.Background(), p, "writer", s.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	if second.ManifestID != first.ManifestID {
		t.Fatal("identical context not reused")
	}
	eventsAfter, err := journal.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatal("reuse appended a duplicate event")
	}
	changes := []fileeffects.Change{{Path: "file.txt", BeforeHash: digestText("base\n"), ContentBase64: content64("repaired\n")}}
	prep, err := PrepareFiles(context.Background(), p, changes)
	if err != nil {
		t.Fatal(err)
	}
	auth := authorize(t, prep)
	if _, err := ApplyFiles(context.Background(), p, prep, auth); err != nil {
		t.Fatal(err)
	}
	repaired, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	newCandidate, _ := repaired.Candidate.ID()
	oldCandidate, _ := s.Candidate.ID()
	if newCandidate == oldCandidate {
		t.Fatal("fixture made no candidate progress")
	}
	role := writerTaskRole(repaired)
	rec, err := AdmitTaskContext(context.Background(), p, role, repaired.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	if rec.CandidateID != newCandidate || rec.Role != role || rec.ManifestID == first.ManifestID {
		t.Fatalf("repair context not bound to new candidate: %+v", rec)
	}
	winv, err := PrepareWriterInvocation(p)
	if err != nil {
		t.Fatal(err)
	}
	wm := taskContextInputOf(t, winv)
	wtc, ok := wm["task_context"]
	if !ok {
		t.Fatal("writer prompt lacks repair task_context")
	}
	wraw, _ := json.Marshal(wtc)
	var wdecoded TaskContextRecord
	if err := canonical.Decode(wraw, &wdecoded); err != nil {
		t.Fatalf("repair task_context not canonical: %v", err)
	}
	if wdecoded.ManifestID != rec.ManifestID || len(wdecoded.Manifest.Selected) != len(rec.Manifest.Selected) {
		t.Fatal("writer prompt manifest differs from repair record")
	}
	for i := range rec.Manifest.Selected {
		if wdecoded.Manifest.Selected[i].Content != rec.Manifest.Selected[i].Content || wdecoded.Manifest.Selected[i].Path != rec.Manifest.Selected[i].Path {
			t.Fatalf("repair prompt selected content mismatch at %d", i)
		}
	}
	wrawJSON, _ := json.Marshal(wtc)
	if strings.Contains(string(wrawJSON), oldCandidate) && !strings.Contains(string(wrawJSON), newCandidate) {
		t.Fatal("writer prompt reused stale candidate context")
	}
}

func TestTaskContextUnavailableIsExplicit(t *testing.T) {
	p, _ := boundedTaskFixture(t, map[string]string{".env": "API_KEY=secret\n", "secrets/a.txt": "x\n"})
	rec, err := AdmitTaskContext(context.Background(), p, "explorer", "greeting objective")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Unavailable == "" {
		t.Fatal("empty eligible selection did not yield explicit unavailable context")
	}
	if len(rec.Manifest.Selected) != 0 || rec.ManifestID != "" {
		t.Fatal("unavailable context must not carry selected content")
	}
	inv, err := PrepareExplorerInvocation(p, "greeting objective")
	if err != nil {
		t.Fatal(err)
	}
	m := taskContextInputOf(t, inv)
	tc, ok := m["task_context"]
	if !ok {
		t.Fatal("unavailable context missing from prompt")
	}
	raw, _ := json.Marshal(tc)
	var decoded TaskContextRecord
	if err := canonical.Decode(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Unavailable == "" {
		t.Fatal("prompt lost unavailable evidence")
	}
}

func TestTaskContextPrepareRequiresAdmission(t *testing.T) {
	ctx := context.Background()
	p, s := boundedTaskFixture(t, map[string]string{"file.txt": "base\n"})
	question := "Explain file.txt base"
	before, err := journal.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareExplorerInvocation(p, question); err == nil {
		t.Fatal("explorer prepared without required bounded context")
	} else if !strings.Contains(err.Error(), "admission missing") {
		t.Fatalf("explorer missing-context error not propagated: %v", err)
	}
	if _, err := PrepareWriterInvocation(p); err == nil {
		t.Fatal("writer prepared without required bounded context")
	} else if !strings.Contains(err.Error(), "admission missing") {
		t.Fatalf("writer missing-context error not propagated: %v", err)
	}
	afterMissing, err := journal.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterMissing) != len(before) {
		t.Fatal("failed Prepare changed the journal; Prepare must not admit or read the filesystem")
	}
	// Direct entrypoints admit required context before preparing.
	if err := maybeAdmitTaskContext(ctx, p, "explorer", question); err != nil {
		t.Fatal(err)
	}
	s, err = Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := maybeAdmitTaskContext(ctx, p, writerTaskRole(s), s.Creation.Objective); err != nil {
		t.Fatal(err)
	}
	admitted, err := journal.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(admitted) == len(before) {
		t.Fatal("admission changed nothing; expected task.context-admitted events")
	}
	explorerInv, err := PrepareExplorerInvocation(p, question)
	if err != nil {
		t.Fatal(err)
	}
	m := taskContextInputOf(t, explorerInv)
	tc, ok := m["task_context"]
	if !ok {
		t.Fatal("explorer prompt lacks admitted task_context")
	}
	raw, _ := json.Marshal(tc)
	var decoded TaskContextRecord
	if err := canonical.Decode(raw, &decoded); err != nil {
		t.Fatalf("explorer task_context not canonical: %v", err)
	}
	s, err = Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	want, err := taskContextForRole(s, "explorer", question)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ManifestID != want.ManifestID || len(decoded.Manifest.Selected) != len(want.Manifest.Selected) {
		t.Fatal("explorer prompt manifest differs from persisted record")
	}
	for i := range want.Manifest.Selected {
		if decoded.Manifest.Selected[i].Content != want.Manifest.Selected[i].Content || decoded.Manifest.Selected[i].Path != want.Manifest.Selected[i].Path {
			t.Fatalf("explorer prompt selected content mismatch at %d", i)
		}
	}
	writerInv, err := PrepareWriterInvocation(p)
	if err != nil {
		t.Fatal(err)
	}
	wm := taskContextInputOf(t, writerInv)
	wtc, ok := wm["task_context"]
	if !ok {
		t.Fatal("writer prompt lacks admitted task_context")
	}
	wraw, _ := json.Marshal(wtc)
	var wdecoded TaskContextRecord
	if err := canonical.Decode(wraw, &wdecoded); err != nil {
		t.Fatalf("writer task_context not canonical: %v", err)
	}
	s, err = Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	wwant, err := taskContextForRole(s, writerTaskRole(s), s.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	if wdecoded.ManifestID != wwant.ManifestID {
		t.Fatal("writer prompt manifest differs from persisted record")
	}
	final, err := journal.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(final) != len(admitted) {
		t.Fatal("Prepare performed admission; Prepare and replay must perform no filesystem reads")
	}
}

func TestTaskContextReviewRequiresAdmissionAndLegacy(t *testing.T) {
	ctx := context.Background()
	fastVerification := []config.Check{{Name: "git-version", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}}

	bounded := boundedTaskCreation(t, 2)
	bounded.Config.Verification = fastVerification
	bp, _ := approvedRepositoryCreation(t, bounded)
	if _, err := StartWorkspace(ctx, bp); err != nil {
		t.Fatal(err)
	}
	bs, err := Verify(ctx, bp)
	if err != nil || bs.State != "REVIEWING" {
		t.Fatal("bounded review fixture unavailable", err)
	}
	bBefore, err := journal.Read(bp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareReviewInvocation(bp); err == nil {
		t.Fatal("review prepared without required bounded context")
	} else if !strings.Contains(err.Error(), "admission missing") {
		t.Fatalf("review missing-context error not propagated: %v", err)
	}
	bAfterMissing, err := journal.Read(bp)
	if err != nil {
		t.Fatal(err)
	}
	if len(bAfterMissing) != len(bBefore) {
		t.Fatal("failed review Prepare changed the journal")
	}
	if err := maybeAdmitTaskContext(ctx, bp, "reviewer", bs.Creation.Objective); err != nil {
		t.Fatal(err)
	}
	reviewInv, err := PrepareReviewInvocation(bp)
	if err != nil {
		t.Fatal(err)
	}
	rm := taskContextInputOf(t, reviewInv)
	rtc, ok := rm["task_context"]
	if !ok {
		t.Fatal("review prompt lacks admitted task_context")
	}
	rraw, _ := json.Marshal(rtc)
	var rdecoded TaskContextRecord
	if err := canonical.Decode(rraw, &rdecoded); err != nil {
		t.Fatalf("review task_context not canonical: %v", err)
	}
	rs, err := Inspect(bp)
	if err != nil {
		t.Fatal(err)
	}
	rwant, err := taskContextForRole(rs, "reviewer", rs.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	if rdecoded.ManifestID != rwant.ManifestID || len(rdecoded.Manifest.Selected) != len(rwant.Manifest.Selected) {
		t.Fatal("review prompt manifest differs from persisted record")
	}

	legacy := creation(t)
	legacy.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-writer", Effort: "high", Role: "writer"}
	legacy.Config.Reviewer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-reviewer", Effort: "high", Role: "reviewer"}
	legacy.Config.Verification = fastVerification
	lp, _ := approvedRepositoryCreation(t, legacy)
	if _, err := StartWorkspace(ctx, lp); err != nil {
		t.Fatal(err)
	}
	ls, err := Verify(ctx, lp)
	if err != nil || ls.State != "REVIEWING" {
		t.Fatal("legacy review fixture unavailable", err)
	}
	legacyInv, err := PrepareReviewInvocation(lp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(legacyInv.Input, "task_context") {
		t.Fatal("legacy review invocation carries task_context")
	}
	lBefore, err := journal.Read(lp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitTaskContext(ctx, lp, "reviewer", ls.Creation.Objective); err == nil {
		t.Fatal("legacy run admitted task context")
	}
	if err := maybeAdmitTaskContext(ctx, lp, "reviewer", ls.Creation.Objective); err != nil {
		t.Fatal(err)
	}
	lAfter, err := journal.Read(lp)
	if err != nil {
		t.Fatal(err)
	}
	if len(lAfter) != len(lBefore) {
		t.Fatal("legacy review admission changed the journal")
	}
}

func countTaskContextAdmissions(t *testing.T, path string) int {
	t.Helper()
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range events {
		if e.Kind == "task.context-admitted" {
			n++
		}
	}
	return n
}

func TestTaskContextDirectDispatchAdmitsBeforeInvocation(t *testing.T) {
	ctx := context.Background()
	// Bounded run with fake role runtimes: the provider is blocked at the
	// host-evidence check (no Codex host), so no real model is dispatched.
	// Direct Run* entrypoints must still admit the required bounded context
	// before invocation construction.
	p, _ := boundedTaskFixture(t, map[string]string{"file.txt": "base\n"})
	question := "Explain file.txt base"
	if _, err := PrepareExplorerInvocation(p, question); err == nil {
		t.Fatal("expected missing admission before direct dispatch")
	}
	if _, err := RunExplorer(ctx, p, question); err == nil {
		t.Fatal("expected host-blocked explorer dispatch")
	} else if strings.Contains(err.Error(), "admission missing") {
		t.Fatalf("direct explorer dispatch did not admit before invocation: %v", err)
	}
	if n := countTaskContextAdmissions(t, p); n != 1 {
		t.Fatalf("direct explorer dispatch admitted %d contexts, want 1", n)
	}
	inv, err := PrepareExplorerInvocation(p, question)
	if err != nil {
		t.Fatal(err)
	}
	m := taskContextInputOf(t, inv)
	raw, _ := json.Marshal(m["task_context"])
	var decoded TaskContextRecord
	if err := canonical.Decode(raw, &decoded); err != nil {
		t.Fatalf("direct explorer admission not canonical: %v", err)
	}
	s, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	want, err := taskContextForRole(s, "explorer", question)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ManifestID != want.ManifestID || len(decoded.Manifest.Selected) != len(want.Manifest.Selected) {
		t.Fatal("direct explorer admission manifest differs from persisted record")
	}
	for i := range want.Manifest.Selected {
		if decoded.Manifest.Selected[i].Content != want.Manifest.Selected[i].Content || decoded.Manifest.Selected[i].Path != want.Manifest.Selected[i].Path {
			t.Fatalf("direct explorer selected content mismatch at %d", i)
		}
	}
	// Reuse: a second direct dispatch must not append a duplicate admission.
	if _, err := RunExplorer(ctx, p, question); err == nil {
		t.Fatal("expected host-blocked explorer re-dispatch")
	}
	if n := countTaskContextAdmissions(t, p); n != 1 {
		t.Fatalf("explorer reuse appended duplicate admission: %d", n)
	}
	// Writer admits its own role record through the same direct path.
	if _, err := RunWriter(ctx, p); err == nil {
		t.Fatal("expected host-blocked writer dispatch")
	} else if strings.Contains(err.Error(), "admission missing") {
		t.Fatalf("direct writer dispatch did not admit before invocation: %v", err)
	}
	s, err = Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	wwant, err := taskContextForRole(s, writerTaskRole(s), s.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	winv, err := PrepareWriterInvocation(p)
	if err != nil {
		t.Fatal(err)
	}
	wm := taskContextInputOf(t, winv)
	wraw, _ := json.Marshal(wm["task_context"])
	var wdecoded TaskContextRecord
	if err := canonical.Decode(wraw, &wdecoded); err != nil {
		t.Fatalf("direct writer admission not canonical: %v", err)
	}
	if wdecoded.ManifestID != wwant.ManifestID {
		t.Fatal("direct writer admission manifest differs from persisted record")
	}
	if n := countTaskContextAdmissions(t, p); n != 2 {
		t.Fatalf("expected explorer+writer admissions, got %d", n)
	}
	// Reviewer admits through the direct path on a REVIEWING fixture.
	fastVerification := []config.Check{{Name: "git-version", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}}
	bounded := boundedTaskCreation(t, 2)
	bounded.Config.Verification = fastVerification
	bp, _ := approvedRepositoryCreation(t, bounded)
	if _, err := StartWorkspace(ctx, bp); err != nil {
		t.Fatal(err)
	}
	bs, err := Verify(ctx, bp)
	if err != nil || bs.State != "REVIEWING" {
		t.Fatal("bounded review fixture unavailable", err)
	}
	if _, err := RunReview(ctx, bp); err == nil {
		t.Fatal("expected host-blocked review dispatch")
	} else if strings.Contains(err.Error(), "admission missing") {
		t.Fatalf("direct review dispatch did not admit before invocation: %v", err)
	}
	if n := countTaskContextAdmissions(t, bp); n != 1 {
		t.Fatalf("direct review dispatch admitted %d contexts, want 1", n)
	}
	rinv, err := PrepareReviewInvocation(bp)
	if err != nil {
		t.Fatal(err)
	}
	rm := taskContextInputOf(t, rinv)
	rraw, _ := json.Marshal(rm["task_context"])
	var rdecoded TaskContextRecord
	if err := canonical.Decode(rraw, &rdecoded); err != nil {
		t.Fatalf("direct review admission not canonical: %v", err)
	}
	rs, err := Inspect(bp)
	if err != nil {
		t.Fatal(err)
	}
	rwant, err := taskContextForRole(rs, "reviewer", rs.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	if rdecoded.ManifestID != rwant.ManifestID || len(rdecoded.Manifest.Selected) != len(rwant.Manifest.Selected) {
		t.Fatal("direct review admission manifest differs from persisted record")
	}
	// Legacy disabled: direct dispatch adds no effects and changes no identities.
	legacy := creation(t)
	legacy.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-writer", Effort: "high", Role: "writer"}
	legacy.Config.Explorer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-explorer", Effort: "low", Role: "explorer"}
	lp, _ := approvedRepositoryCreation(t, legacy)
	if _, err := StartWorkspace(ctx, lp); err != nil {
		t.Fatal(err)
	}
	lBefore, err := journal.Read(lp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunExplorer(ctx, lp, question); err == nil {
		t.Fatal("expected host-blocked legacy explorer dispatch")
	}
	if _, err := RunWriter(ctx, lp); err == nil {
		t.Fatal("expected host-blocked legacy writer dispatch")
	}
	lAfter, err := journal.Read(lp)
	if err != nil {
		t.Fatal(err)
	}
	if len(lAfter) != len(lBefore) {
		t.Fatal("legacy direct dispatch added effects")
	}
	if n := countTaskContextAdmissions(t, lp); n != 0 {
		t.Fatalf("legacy direct dispatch admitted %d contexts", n)
	}
	legacyExplorer, err := PrepareExplorerInvocation(lp, question)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(legacyExplorer.Input, "task_context") {
		t.Fatal("legacy direct dispatch changed explorer identity")
	}
	legacyWriter, err := PrepareWriterInvocation(lp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(legacyWriter.Input, "task_context") {
		t.Fatal("legacy direct dispatch changed writer identity")
	}
}

func TestTaskContextDirectDispatchBlockedAdmitsNothing(t *testing.T) {
	ctx := context.Background()
	p, _ := boundedTaskFixture(t, map[string]string{"file.txt": "base\n"})
	s, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := verification.PreparePlan(s.RunID, "blocked-pending-fixture", candidateID, s.Workspace.Request.Path, s.Creation.Config.Verification)
	if err != nil {
		t.Fatal(err)
	}
	planID, err := plan.ID()
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(p, "verification.planned", plan); err != nil {
		t.Fatal(err)
	}
	if err := Append(p, "verification.started", VerificationStart{planID, 0}); err != nil {
		t.Fatal(err)
	}
	blocked, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := autonomousDispatchBlocked(blocked); err == nil {
		t.Fatal("pending verification fixture not blocked")
	}
	before, err := journal.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	admissionsBefore := countTaskContextAdmissions(t, p)
	probe := "Explain file.txt base blocked probe"
	if _, err := RunExplorer(ctx, p, probe); err == nil {
		t.Fatal("blocked explorer dispatch admitted work")
	} else if !strings.Contains(err.Error(), "pending") {
		t.Fatalf("expected dispatch-blocked error, got: %v", err)
	}
	if _, err := RunWriter(ctx, p); err == nil {
		t.Fatal("blocked writer dispatch admitted work")
	} else if !strings.Contains(err.Error(), "pending") {
		t.Fatalf("expected dispatch-blocked error, got: %v", err)
	}
	after, err := journal.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatal("blocked direct dispatch added effects")
	}
	if n := countTaskContextAdmissions(t, p); n != admissionsBefore {
		t.Fatal("blocked direct dispatch admitted new context")
	}
}
