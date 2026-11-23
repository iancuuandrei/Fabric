package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/evidencevalue"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

func taskSelectedFile(path string) taskcontext.SelectedFile {
	return taskcontext.SelectedFile{Path: path, Hash: strings.Repeat("a", 64), Start: 0, End: 1, ExcerptHash: strings.Repeat("b", 64), Reason: "path_hint", Content: "x"}
}

func evidenceHandoffRequest(t *testing.T, path, actionID, query string) EvidenceContextRequest {
	t.Helper()
	s, head, err := InspectWithHead(path)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := EvidenceBinding(s, head)
	if err != nil {
		t.Fatal(err)
	}
	cost := 1.0
	model := evidencevalue.Request{Version: 1, Binding: binding, Resources: []evidencevalue.Resource{{Name: "local_compute_ms", Limit: 100, Price: 1}}, Actions: []evidencevalue.Action{{ID: actionID, Kind: "source_read", Reliability: evidencevalue.Reliability{Ordinal: 2}, Outcomes: []evidencevalue.Outcome{{Probability: .5, Utilities: []float64{1, 0}}, {Probability: .5, Utilities: []float64{0, 1}}}, Costs: map[string]*float64{"local_compute_ms": &cost}}}}
	return EvidenceContextRequest{Model: evidencevalue.EncodeRequest(model), Queries: map[string]string{actionID: query}}
}

func handoffFixture(t *testing.T) (string, Snapshot) {
	t.Helper()
	return boundedTaskFixture(t, map[string]string{
		"greeting.txt":      "greeting tested greeting implementation\n",
		"target/hidden.txt": "zephyr quux hidden vault mechanism\n",
		"notes.txt":         "ordinary notes\n",
	})
}

func selectedPaths(rec TaskContextRecord) []string {
	out := make([]string, 0, len(rec.Manifest.Selected))
	for _, sel := range rec.Manifest.Selected {
		out = append(out, sel.Path)
	}
	return out
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

func TestEvidenceHintHandoffToDifferentRoleQuery(t *testing.T) {
	ctx := context.Background()
	path, s := handoffFixture(t)
	objective := s.Creation.Objective
	evcQuery := "Explain zephyr quux hidden vault mechanism"

	baseline, err := AdmitTaskContext(ctx, path, "reviewer", objective)
	if err != nil {
		t.Fatal(err)
	}
	if containsPath(selectedPaths(baseline), "target/hidden.txt") {
		t.Fatalf("baseline reviewer unexpectedly selected EVC target without hints: %v", selectedPaths(baseline))
	}

	request := evidenceHandoffRequest(t, path, "inspect-source", evcQuery)
	result, err := AcquireEvidenceContext(ctx, path, request)
	if err != nil || result.Context == nil || result.Decision.Report.Selected != "inspect-source" {
		t.Fatal("positive decision did not acquire", result, err)
	}
	if result.Context.EvidenceDecisionID != result.Decision.ID {
		t.Fatal("acquired context missing decision linkage")
	}
	if !containsPath(selectedPaths(*result.Context), "target/hidden.txt") {
		t.Fatalf("EVC explorer did not select intended source: %v", selectedPaths(*result.Context))
	}

	after, err := AdmitTaskContext(ctx, path, "writer", objective)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(selectedPaths(after), "target/hidden.txt") {
		t.Fatalf("writer did not reuse EVC hint for different query: %v", selectedPaths(after))
	}
	if len(after.Manifest.Selected) == 0 || len(after.Manifest.Selected) > 12 || after.Manifest.SelectedBytes > 48<<10 {
		t.Fatalf("writer selection left existing bounds: %+v", after.Manifest)
	}
	foundHint := false
	for _, sel := range after.Manifest.Selected {
		if sel.Path == "target/hidden.txt" && sel.Reason == "path_hint" {
			foundHint = true
		}
	}
	if !foundHint {
		t.Fatalf("intended source lacks advisory hint reason: %+v", after.Manifest.Selected)
	}

	snap, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.EvidenceDecisions) != 1 || len(snap.TaskContexts) != 3 {
		t.Fatalf("expected decision plus reviewer/explorer/writer contexts, got decisions=%d contexts=%d", len(snap.EvidenceDecisions), len(snap.TaskContexts))
	}
	if _, err := taskContextForRole(snap, "writer", objective); err != nil {
		t.Fatal("admitted writer context not replayable", err)
	}
	if _, err := taskContextForRole(snap, "explorer", evcQuery); err != nil {
		t.Fatal("acquired explorer context not replayable", err)
	}
	winv, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	var wm map[string]any
	if err := json.Unmarshal([]byte(winv.Input), &wm); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(wm["task_context"])
	var bound TaskContextRecord
	if err := canonical.Decode(raw, &bound); err != nil {
		t.Fatalf("writer task context is not canonical: %v", err)
	}
	if bound.ManifestID != after.ManifestID {
		t.Fatal("writer prompt manifest differs from persisted record")
	}
	einv, err := PrepareExplorerInvocation(path, evcQuery)
	if err != nil {
		t.Fatal(err)
	}
	var em map[string]any
	if err := json.Unmarshal([]byte(einv.Input), &em); err != nil {
		t.Fatal(err)
	}
	eraw, _ := json.Marshal(em["task_context"])
	var ebound TaskContextRecord
	if err := canonical.Decode(eraw, &ebound); err != nil {
		t.Fatal(err)
	}
	if ebound.ManifestID != result.Context.ManifestID || ebound.EvidenceDecisionID != result.Decision.ID {
		t.Fatal("explorer prompt lost decision-bound admission")
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := Replay(events)
	if err != nil {
		t.Fatal("journal replay failed after hint reuse", err)
	}
	if len(replayed.EvidenceDecisions) != 1 || len(replayed.TaskContexts) != 3 {
		t.Fatal("replay lost decision or context evidence")
	}
}

func TestEvidenceHintDedupAndOrder(t *testing.T) {
	source := strings.Repeat("a", 64)
	candidate := strings.Repeat("b", 64)
	decision := EvidenceContextDecision{
		ID: strings.Repeat("c", 64),
		Request: EvidenceContextRequest{
			Model:   evidencevalue.WireRequest{Binding: evidencevalue.Binding{RunID: strings.Repeat("d", 64), SourceID: source, CandidateID: candidate, JournalHead: strings.Repeat("e", 64)}},
			Queries: map[string]string{"inspect-source": "Explain zephyr quux"},
		},
		Report: evidencevalue.WireReport{Selected: "inspect-source"},
	}
	other := decision
	other.ID = strings.Repeat("f", 64)
	mkCtx := func(id string, paths ...string) TaskContextRecord {
		rec := TaskContextRecord{EvidenceDecisionID: id, Role: "explorer", SourceID: source, CandidateID: candidate, Query: "Explain zephyr quux"}
		for _, p := range paths {
			rec.Manifest.Selected = append(rec.Manifest.Selected, taskSelectedFile(p))
		}
		return rec
	}
	_ = other
	s := Snapshot{
		EvidenceDecisions: []EvidenceContextDecision{decision},
		TaskContexts:      []TaskContextRecord{mkCtx(decision.ID, "zulu.txt", "alpha.txt", "zulu.txt", "middle/nested.txt")},
	}
	got := taskContextEvidencePathsForCandidate(s, source, candidate)
	want := []string{"alpha.txt", "middle/nested.txt", "zulu.txt"}
	if len(got) != len(want) {
		t.Fatalf("dedup/order mismatch: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedup/order mismatch: got %v want %v", got, want)
		}
	}
	merged := mergeTaskContextHintPaths([]string{"zulu.txt", "alpha.txt"}, []string{"alpha.txt", "beta.txt"})
	if len(merged) != 3 || merged[0] != "alpha.txt" || merged[1] != "beta.txt" || merged[2] != "zulu.txt" {
		t.Fatalf("hint merge not deduped/sorted: %v", merged)
	}
}

func TestEvidenceHintExclusions(t *testing.T) {
	source := strings.Repeat("a", 64)
	candidate := strings.Repeat("b", 64)
	staleCandidate := strings.Repeat("9", 64)
	validQuery := "Explain zephyr quux"
	mkDecision := func(id, selected, query, bindSource, bindCandidate string) EvidenceContextDecision {
		queries := map[string]string{}
		if query != "" {
			queries[selected] = query
		}
		return EvidenceContextDecision{
			ID:      id,
			Request: EvidenceContextRequest{Model: evidencevalue.WireRequest{Binding: evidencevalue.Binding{SourceID: bindSource, CandidateID: bindCandidate}}, Queries: queries},
			Report:  evidencevalue.WireReport{Selected: selected},
		}
	}
	mkCtx := func(decisionID, role, sourceID, candidateID, query, unavailable string, selected int) TaskContextRecord {
		rec := TaskContextRecord{EvidenceDecisionID: decisionID, Role: role, SourceID: sourceID, CandidateID: candidateID, Query: query, Unavailable: unavailable}
		for i := 0; i < selected; i++ {
			rec.Manifest.Selected = append(rec.Manifest.Selected, taskSelectedFile("target/hidden.txt"))
		}
		return rec
	}
	cases := map[string]Snapshot{
		"no-positive": {
			EvidenceDecisions: []EvidenceContextDecision{mkDecision(strings.Repeat("c", 64), "", "", source, candidate)},
			TaskContexts:      []TaskContextRecord{mkCtx(strings.Repeat("c", 64), "explorer", source, candidate, validQuery, "", 1)},
		},
		"missing-context": {
			EvidenceDecisions: []EvidenceContextDecision{mkDecision(strings.Repeat("c", 64), "inspect-source", validQuery, source, candidate)},
		},
		"wrong-role": {
			EvidenceDecisions: []EvidenceContextDecision{mkDecision(strings.Repeat("c", 64), "inspect-source", validQuery, source, candidate)},
			TaskContexts:      []TaskContextRecord{mkCtx(strings.Repeat("c", 64), "writer", source, candidate, validQuery, "", 1)},
		},
		"wrong-query": {
			EvidenceDecisions: []EvidenceContextDecision{mkDecision(strings.Repeat("c", 64), "inspect-source", validQuery, source, candidate)},
			TaskContexts:      []TaskContextRecord{mkCtx(strings.Repeat("c", 64), "explorer", source, candidate, "different question", "", 1)},
		},
		"stale-candidate-decision": {
			EvidenceDecisions: []EvidenceContextDecision{mkDecision(strings.Repeat("c", 64), "inspect-source", validQuery, source, staleCandidate)},
			TaskContexts:      []TaskContextRecord{mkCtx(strings.Repeat("c", 64), "explorer", source, candidate, validQuery, "", 1)},
		},
		"stale-source-context": {
			EvidenceDecisions: []EvidenceContextDecision{mkDecision(strings.Repeat("c", 64), "inspect-source", validQuery, source, candidate)},
			TaskContexts:      []TaskContextRecord{mkCtx(strings.Repeat("c", 64), "explorer", strings.Repeat("8", 64), candidate, validQuery, "", 1)},
		},
		"malformed-linkage": {
			EvidenceDecisions: []EvidenceContextDecision{mkDecision(strings.Repeat("c", 64), "inspect-source", validQuery, source, candidate)},
			TaskContexts:      []TaskContextRecord{mkCtx(strings.Repeat("7", 64), "explorer", source, candidate, validQuery, "", 1)},
		},
		"unavailable-context": {
			EvidenceDecisions: []EvidenceContextDecision{mkDecision(strings.Repeat("c", 64), "inspect-source", validQuery, source, candidate)},
			TaskContexts:      []TaskContextRecord{mkCtx(strings.Repeat("c", 64), "explorer", source, candidate, validQuery, "no_eligible_files", 0)},
		},
		"empty-selection": {
			EvidenceDecisions: []EvidenceContextDecision{mkDecision(strings.Repeat("c", 64), "inspect-source", validQuery, source, candidate)},
			TaskContexts:      []TaskContextRecord{mkCtx(strings.Repeat("c", 64), "explorer", source, candidate, validQuery, "", 0)},
		},
	}
	for name, snap := range cases {
		if got := taskContextEvidencePathsForCandidate(snap, source, candidate); len(got) != 0 {
			t.Fatalf("%s contributed hints: %v", name, got)
		}
	}
	valid := Snapshot{
		EvidenceDecisions: []EvidenceContextDecision{mkDecision(strings.Repeat("c", 64), "inspect-source", validQuery, source, candidate)},
		TaskContexts:      []TaskContextRecord{mkCtx(strings.Repeat("c", 64), "explorer", source, candidate, validQuery, "", 1)},
	}
	if got := taskContextEvidencePathsForCandidate(valid, source, candidate); len(got) != 1 || got[0] != "target/hidden.txt" {
		t.Fatalf("valid linkage excluded: %v", got)
	}
}

func TestEvidenceHintSameQueryReuse(t *testing.T) {
	ctx := context.Background()
	path, s := handoffFixture(t)
	query := "Explain zephyr quux hidden vault mechanism"
	pre, err := AdmitTaskContext(ctx, path, "explorer", query)
	if err != nil {
		t.Fatal(err)
	}
	if pre.EvidenceDecisionID != "" {
		t.Fatal("pre-decision context unexpectedly linked")
	}
	request := evidenceHandoffRequest(t, path, "inspect-source", query)
	result, err := AcquireEvidenceContext(ctx, path, request)
	if err != nil || result.Context == nil {
		t.Fatal("reuse acquisition failed", result, err)
	}
	if result.Context.ManifestID != pre.ManifestID {
		t.Fatalf("same-query reuse changed historical record: %s vs %s", result.Context.ManifestID, pre.ManifestID)
	}
	snap, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	sourceID, err := snap.Creation.Repository.ID()
	if err != nil {
		t.Fatal(err)
	}
	candidateID, err := snap.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	hints := taskContextEvidencePathsForCandidate(snap, sourceID, candidateID)
	if !containsPath(hints, "target/hidden.txt") {
		t.Fatalf("reused context without decision ID contributed no hints: %v", hints)
	}
	after, err := AdmitTaskContext(ctx, path, "writer", s.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(selectedPaths(after), "target/hidden.txt") {
		t.Fatalf("writer missed reused EVC hint: %v", selectedPaths(after))
	}
}

func TestEvidenceHintAbsentFeatureUnchanged(t *testing.T) {
	ctx := context.Background()
	path, s := boundedTaskFixture(t, map[string]string{"file.txt": "base\n"})
	snap, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	sourceID, err := snap.Creation.Repository.ID()
	if err != nil {
		t.Fatal(err)
	}
	candidateID, err := snap.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	if got := taskContextEvidencePathsForCandidate(snap, sourceID, candidateID); len(got) != 0 {
		t.Fatalf("absent EVC decisions contributed hints: %v", got)
	}
	if got := mergeTaskContextHintPaths(taskContextExplorationPaths(snap), nil); len(got) != len(taskContextExplorationPaths(snap)) {
		t.Fatal("empty evidence merge changed legacy exploration hints")
	}
	rec, err := AdmitTaskContext(ctx, path, "explorer", "Explain file.txt base")
	if err != nil {
		t.Fatal(err)
	}
	if rec.EvidenceDecisionID != "" {
		t.Fatal("ordinary admission gained decision linkage")
	}
	if _, err := PrepareExplorerInvocation(path, "Explain file.txt base"); err != nil {
		t.Fatal(err)
	}
	_ = s
}
