package control

import (
	"context"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/taskcontext"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func rrfBoundedCreation(t *testing.T, maxRepairs int) Creation {
	t.Helper()
	c := boundedTaskCreation(t, maxRepairs)
	c.Execution.ContextSelector = taskContextSelectorRRFCoverageV1
	if err := c.Execution.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTaskContextRRFCoverageAdmitsWriterAndBindsInvocation(t *testing.T) {
	ctx := context.Background()
	c := rrfBoundedCreation(t, 2)
	p, s := boundedTaskFixtureWithCreation(t, c, map[string]string{
		"internal/greeting/greeting.go":      "package greeting\nfunc Hello(name string) string { return \"Hello, \" + name }\n",
		"internal/greeting/greeting_test.go": "package greeting\nfunc TestHelloTrimmed(t *testing.T) {}\n",
		"README.md":                          "Fabric greeting usage\n",
	})
	record, err := AdmitTaskContext(ctx, p, "writer", s.Creation.Objective)
	if err != nil {
		t.Fatalf("rrf-coverage writer admission failed: %v", err)
	}
	if record.Manifest.Selector != taskContextSelectorRRFCoverageV1 {
		t.Fatalf("manifest selector not bound: %+v", record.Manifest)
	}
	if len(record.Manifest.Selected) == 0 {
		t.Fatal("rrf-coverage selected nothing")
	}
	for _, sel := range record.Manifest.Selected {
		if len(sel.Ranks) == 0 {
			t.Fatalf("selected item lacks ranker provenance: %+v", sel)
		}
	}
	if record.ManifestID == "" {
		t.Fatal("manifest identity missing")
	}
	if _, err := record.Manifest.ID(); err != nil {
		t.Fatal(err)
	}
	s, err = Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	want, err := taskContextForRole(s, writerTaskRole(s), s.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	if want.ManifestID != record.ManifestID || want.Manifest.Selector != taskContextSelectorRRFCoverageV1 {
		t.Fatal("persisted writer context differs from admitted record")
	}
	inv, err := PrepareWriterInvocation(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inv.Input, want.ManifestID) && !strings.Contains(inv.Input, "task_context") {
		t.Fatal("writer invocation lacks admitted task_context binding")
	}
	// Replay substitution: same manifest under a legacy run must be rejected.
	legacy := boundedTaskCreation(t, 2)
	_, ls := boundedTaskFixtureWithCreation(t, legacy, map[string]string{
		"internal/greeting/greeting.go": "package greeting\nfunc Hello() string { return \"hi\" }\n",
	})
	if err := validateTaskContextSelectorBinding(ls, record.Manifest); err == nil {
		t.Fatal("selector substitution across run policies accepted")
	}
}

func TestTaskContextSelectorSubstitutionRejectedOnReplay(t *testing.T) {
	ctx := context.Background()
	c := rrfBoundedCreation(t, 2)
	p, s := boundedTaskFixtureWithCreation(t, c, map[string]string{
		"a.txt": "alpha content\n",
		"b.txt": "beta content\n",
	})
	record, err := AdmitTaskContext(ctx, p, "explorer", "alpha beta explorer question")
	if err != nil {
		t.Fatal(err)
	}
	// Forge the manifest selector to legacy and verify the identity diverges
	// and replay binding rejects the substitution.
	forged := record.Manifest
	forged.Selector = ""
	forgedID, ferr := forged.ID()
	if ferr == nil && forgedID == record.ManifestID {
		t.Fatal("forged legacy manifest shares new-mode identity")
	}
	s, err = Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTaskContextSelectorBinding(s, forged); err == nil {
		t.Fatal("manifest selector substitution accepted")
	}
	_ = s
}

func TestTaskContextSelectorPolicyRejected(t *testing.T) {
	bad := boundedTaskCreation(t, 2)
	bad.Execution.ContextSelector = "unknown-mode"
	if err := bad.Execution.Validate(); err == nil {
		t.Fatal("unknown context selector validated")
	}
	withoutContext := boundedTaskCreation(t, 2)
	withoutContext.Execution.Context = ""
	withoutContext.Execution.ContextSelector = taskContextSelectorRRFCoverageV1
	if err := withoutContext.Execution.Validate(); err == nil {
		t.Fatal("selector without bounded-v1 validated")
	}
	// Malformed run.created must be rejected at replay; no run is admitted.
	if err := Append(t.TempDir()+"/bad.jsonl", "run.created", withoutContext); err == nil {
		t.Fatal("malformed selector policy admitted at run.created")
	}
}

func TestTaskContextLegacyEmptyPreservesReplay(t *testing.T) {
	ctx := context.Background()
	p, s := boundedTaskFixture(t, map[string]string{
		"a.txt": "alpha content\n",
	})
	record, err := AdmitTaskContext(ctx, p, "writer", s.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	if record.Manifest.Selector != "" {
		t.Fatal("legacy admission carries selector")
	}
	for _, sel := range record.Manifest.Selected {
		if len(sel.Ranks) != 0 || len(sel.Covered) != 0 {
			t.Fatalf("legacy selection carries provenance: %+v", sel)
		}
	}
}

func TestTaskContextIsolatedScopeRequiresBinding(t *testing.T) {
	ctx := context.Background()
	c := rrfBoundedCreation(t, 2)
	p, _ := boundedTaskFixtureWithCreation(t, c, map[string]string{"a.txt": "alpha\n"})
	if _, err := admitTaskContext(ctx, p, "writer", "alpha question", "missing-task"); err == nil {
		t.Fatal("isolated admission without confirmed child accepted")
	}
}

func TestPrioritizeRRFDeterministicAndSkipsAbsent(t *testing.T) {
	states := []worktree.FileState{
		{Path: "a.txt", Hash: strings.Repeat("a", 64)},
		{Path: "b.txt", Hash: strings.Repeat("b", 64)},
		{Path: "c.txt", Hash: strings.Repeat("c", 64)},
	}
	first := prioritizeTaskPathsRRF(states, []string{"b.txt"}, []string{"a.txt"}, nil, "alpha")
	rev := []worktree.FileState{states[2], states[0], states[1]}
	second := prioritizeTaskPathsRRF(rev, []string{"b.txt"}, []string{"a.txt"}, nil, "alpha")
	if len(first) != len(second) {
		t.Fatal("prioritization length changed")
	}
	for i := range first {
		if first[i].Path != second[i].Path {
			t.Fatal("prioritization depends on input order")
		}
	}
	// Absent rankers: no hints/lexical/query matches still orders deterministically.
	plain := prioritizeTaskPathsRRF(states, nil, nil, nil, "qqqzzz")
	if len(plain) != len(states) || plain[0].Path != "a.txt" {
		t.Fatalf("absent-ranker fallback wrong: %+v", plain)
	}
}

func TestTaskContextRRFProvenanceBounded(t *testing.T) {
	if err := taskcontext.ValidateSelector("unknown"); err == nil {
		t.Fatal("unknown selector validated")
	}
	if err := taskcontext.ValidateSelector(""); err != nil {
		t.Fatal(err)
	}
	if err := taskcontext.ValidateSelector(taskContextSelectorRRFCoverageV1); err != nil {
		t.Fatal(err)
	}
}
