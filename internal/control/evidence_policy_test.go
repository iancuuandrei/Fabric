package control

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/evidencevalue"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func writeRepoFile(t *testing.T, root, name, content string) error {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0600)
}

func evidencePolicyTemplate() EvidenceAutoPolicy {
	cost := 1.0
	model := evidencevalue.Request{Version: 1, Resources: []evidencevalue.Resource{{Name: "local_compute_ms", Limit: 100, Price: 1}}, Actions: []evidencevalue.Action{{ID: "inspect-source", Kind: "source_read", Reliability: evidencevalue.Reliability{Ordinal: 2}, Outcomes: []evidencevalue.Outcome{{Probability: .5, Utilities: []float64{1, 0}}, {Probability: .5, Utilities: []float64{0, 1}}}, Costs: map[string]*float64{"local_compute_ms": &cost}}}}
	return EvidenceAutoPolicy{Version: 1, Model: evidencevalue.EncodeRequest(model), Queries: map[string]string{"inspect-source": "Explain file.txt base implementation"}}
}

func graphPolicyFixture(t *testing.T, policy *EvidenceAutoPolicy) (string, Snapshot) {
	t.Helper()
	c := graphCreation(t, 1)
	if policy != nil {
		c.Execution.EvidencePolicy = policy
	}
	path, _ := graphAwaitingApproval(t, c)
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	machineAuthorizePlan(t, path, s)
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	s = recordGraphFixture(t, path)
	return path, s
}

func TestEvidencePolicyTemplateStrictBounds(t *testing.T) {
	valid := evidencePolicyTemplate()
	if err := ValidateEvidenceAutoPolicyTemplate(valid); err != nil {
		t.Fatal("valid template rejected", err)
	}
	for name, mutate := range map[string]func(*EvidenceAutoPolicy){
		"version":       func(p *EvidenceAutoPolicy) { p.Version = 2 },
		"model-version": func(p *EvidenceAutoPolicy) { p.Model.Version = 2 },
		"binding-supplied": func(p *EvidenceAutoPolicy) {
			p.Model.Binding = evidencevalue.Binding{RunID: strings.Repeat("a", 64)}
		},
		"non-source-kind": func(p *EvidenceAutoPolicy) { p.Model.Actions[0].Kind = "spawn_explorer" },
		"missing-query":   func(p *EvidenceAutoPolicy) { delete(p.Queries, "inspect-source") },
		"extra-query":     func(p *EvidenceAutoPolicy) { p.Queries["extra"] = "other" },
		"empty-query":     func(p *EvidenceAutoPolicy) { p.Queries["inspect-source"] = "   " },
		"oversized-query": func(p *EvidenceAutoPolicy) { p.Queries["inspect-source"] = strings.Repeat("x", taskContextQueryCap+1) },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := canonical.Bytes(valid)
			var changed EvidenceAutoPolicy
			if err := json.Unmarshal(raw, &changed); err != nil {
				t.Fatal(err)
			}
			mutate(&changed)
			if err := ValidateEvidenceAutoPolicyTemplate(changed); err == nil {
				t.Fatal("invalid template accepted")
			}
		})
	}
	// Strict wire: unknown and duplicate members rejected before run creation.
	for _, raw := range []string{
		`{"version":1,"model":{"version":1,"binding":{"run_id":"","source_id":"","candidate_id":"","journal_head":""},"resources":[],"actions":[]},"queries":{},"extra":1}`,
		`{"version":1,"version":1,"model":{"version":1,"binding":{"run_id":"","source_id":"","candidate_id":"","journal_head":""},"resources":[],"actions":[]},"queries":{}}`,
	} {
		var policy EvidenceAutoPolicy
		if canonical.Decode([]byte(raw), &policy) == nil {
			t.Fatal("unsafe policy wire accepted")
		}
	}
}

func TestEvidencePolicyLegacySerializationOmitted(t *testing.T) {
	legacy := ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: 2}
	raw, err := canonical.Bytes(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "evidence_policy") {
		t.Fatal("absent policy changed legacy serialization")
	}
	with := legacy
	policy := evidencePolicyTemplate()
	with.Context = taskContextBoundedV1
	with.GraphVersion = 1
	with.MaxParallel = 1
	with.EvidencePolicy = &policy
	if err := with.Validate(); err != nil {
		t.Fatal("valid policy rejected", err)
	}
	for _, bad := range []func(*ExecutionPolicy){
		func(p *ExecutionPolicy) { p.Context = "" },
		func(p *ExecutionPolicy) { p.GraphVersion = 0 },
		func(p *ExecutionPolicy) { p.ParallelImplementationVersion = 1 },
		func(p *ExecutionPolicy) { p.IsolatedImplementationVersion = 1 },
	} {
		clone := with
		bad(&clone)
		if err := clone.Validate(); err == nil {
			t.Fatal("incompatible policy accepted")
		}
	}
}

func TestEvidencePolicyBindingAndReplaySubstitution(t *testing.T) {
	policy := evidencePolicyTemplate()
	path, s := graphPolicyFixture(t, &policy)
	expectedHash, err := EvidenceAutoPolicyHash(policy)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := maybeAcquireEvidencePolicy(context.Background(), path, s)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEvidencePolicyDecision(updated, expectedHash) || len(updated.EvidenceDecisions) != 1 {
		t.Fatal("policy decision not recorded")
	}
	decided := updated.EvidenceDecisions[0]
	if decided.EvidencePolicyHash != expectedHash {
		t.Fatal("policy linkage missing")
	}
	if decided.Report.Selected != "inspect-source" {
		t.Fatal("positive decision not selected")
	}
	// Immutable binding: frozen template retains empty binding while the
	// decision carries the live run/source/candidate/head.
	if policy.Model.Binding != (evidencevalue.Binding{}) {
		t.Fatal("template binding mutated")
	}
	if decided.Request.Model.Binding.RunID != updated.RunID {
		t.Fatal("decision binding not live")
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	index := -1
	for i, e := range events {
		if e.Kind == "evidence.context-decided" {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("decision event missing")
	}
	// Substitution and duplicate rejection under the append semantic lock.
	for _, change := range []func(*EvidenceContextDecision){
		func(d *EvidenceContextDecision) { d.Request.Queries["inspect-source"] = "different question" },
		func(d *EvidenceContextDecision) { d.EvidencePolicyHash = strings.Repeat("a", 64) },
		func(d *EvidenceContextDecision) { d.ID = strings.Repeat("a", 64) },
	} {
		var substituted EvidenceContextDecision
		if err := canonical.Decode(events[index].Payload, &substituted); err != nil {
			t.Fatal(err)
		}
		change(&substituted)
		raw, _ := canonical.Bytes(substituted)
		prefix, err := Replay(events[:index])
		if err != nil {
			t.Fatal(err)
		}
		event := events[index]
		event.Payload = raw
		if replayEvidenceContextDecision(&prefix, event) == nil {
			t.Fatal("substituted policy decision accepted")
		}
	}
	// Tag removal bypass rejection: stripping the exact policy tag from a
	// matching-template event must fail replay even though the stripped
	// request still matches the frozen template.
	for _, strip := range []func(*EvidenceContextDecision){
		func(d *EvidenceContextDecision) { d.EvidencePolicyHash = "" },
		func(d *EvidenceContextDecision) {
			d.EvidencePolicyHash = ""
			untagged, err := evidenceContextDecisionIDFor(d.Request, d.Report, "")
			if err != nil {
				panic(err)
			}
			d.ID = untagged
		},
	} {
		var stripped EvidenceContextDecision
		if err := canonical.Decode(events[index].Payload, &stripped); err != nil {
			t.Fatal(err)
		}
		strip(&stripped)
		raw, _ := canonical.Bytes(stripped)
		prefix, err := Replay(events[:index])
		if err != nil {
			t.Fatal(err)
		}
		event := events[index]
		event.Payload = raw
		if replayEvidenceContextDecision(&prefix, event) == nil {
			t.Fatal("tag-removal policy decision accepted")
		}
		if err := Append(path, "evidence.context-decided", stripped); err == nil {
			t.Fatal("tag-removal Append accepted")
		}
	}
	// Decision identity binds the optional tag while preserving exact
	// absent-policy/manual identities via omitempty.
	bound, err := evidenceContextDecisionIDFor(decided.Request, decided.Report, expectedHash)
	if err != nil || bound != decided.ID {
		t.Fatal("tagged decision identity does not bind policy tag", err)
	}
	untagged, err := evidenceContextDecisionIDFor(decided.Request, decided.Report, "")
	if err != nil || untagged == decided.ID {
		t.Fatal("tagged identity does not differ from untagged", err)
	}
	legacy, err := canonical.Hash("harness.evidence-context-decision.v1", struct {
		Request EvidenceContextRequest   `json:"request"`
		Report  evidencevalue.WireReport `json:"report"`
	}{decided.Request, decided.Report})
	if err != nil || legacy != untagged {
		t.Fatal("empty tag changed historical decision identity", err)
	}
	// Duplicate replay against the recorded prefix must also fail.
	{
		var duplicate EvidenceContextDecision
		if err := canonical.Decode(events[index].Payload, &duplicate); err != nil {
			t.Fatal(err)
		}
		raw, _ := canonical.Bytes(duplicate)
		full, err := Replay(events)
		if err != nil {
			t.Fatal(err)
		}
		event := events[index]
		event.Payload = raw
		if replayEvidenceContextDecision(&full, event) == nil {
			t.Fatal("duplicate policy replay accepted")
		}
	}
	before, _ := journal.Read(path)
	if _, err := AcquireEvidenceContext(context.Background(), path, evidencePolicyLiveRequest(t, path, policy)); err == nil {
		t.Fatal("duplicate policy decision accepted")
	}
	after, _ := journal.Read(path)
	a, _ := canonical.Bytes(before)
	b, _ := canonical.Bytes(after)
	if !bytes.Equal(a, b) {
		t.Fatal("duplicate rejection mutated journal")
	}
}

func TestEvidencePolicyHardFailurePropagatesAndResumeSkips(t *testing.T) {
	path, s := graphPolicyFixture(t, func() *EvidenceAutoPolicy { p := evidencePolicyTemplate(); return &p }())
	expectedHash, err := EvidenceAutoPolicyHash(evidencePolicyTemplate())
	if err != nil {
		t.Fatal(err)
	}
	// TEST-ONLY cancellation: report Canceled once the journal already holds
	// the policy decision. Production code only checks ctx.Err outside append.
	cancelling := &cancelAfterPolicyDecisionContext{Context: context.Background(), path: path, hash: expectedHash}
	failed, err := maybeAcquireEvidencePolicy(cancelling, path, s)
	if err == nil || !strings.Contains(err.Error(), "canceled") && !strings.Contains(err.Error(), "Canceled") && !strings.Contains(strings.ToLower(err.Error()), "cancel") {
		t.Fatalf("after-decision cancellation did not propagate: %v", err)
	}
	if !hasEvidencePolicyDecision(failed, expectedHash) || len(failed.EvidenceDecisions) != 1 {
		t.Fatal("failed acquisition lost retained policy decision")
	}
	if !strings.Contains(err.Error(), failed.EvidenceDecisions[0].ID) {
		t.Fatal("propagated error hides retained decision identity")
	}
	decidedCount := countJournalKind(t, path, "evidence.context-decided")
	if decidedCount != 1 {
		t.Fatalf("expected one retained decision, got %d", decidedCount)
	}
	// Resume with a live context must not repeat acquisition; absence of
	// optional context degrades to ordinary required role context.
	resumed, err := maybeAcquireEvidencePolicy(context.Background(), path, failed)
	if err != nil {
		t.Fatal("resume repeated failed acquisition", err)
	}
	if !hasEvidencePolicyDecision(resumed, expectedHash) || len(resumed.EvidenceDecisions) != 1 {
		t.Fatal("resume lost retained decision")
	}
	if countJournalKind(t, path, "evidence.context-decided") != 1 {
		t.Fatal("resume repeated automatic acquisition")
	}
	if _, err := AdmitTaskContext(context.Background(), path, "writer", resumed.Creation.Objective); err != nil {
		t.Fatal("mandatory writer context blocked after retained failure", err)
	}
}

type cancelAfterPolicyDecisionContext struct {
	context.Context
	path string
	hash string
}

func (c *cancelAfterPolicyDecisionContext) Err() error {
	events, err := journal.Read(c.path)
	if err != nil {
		return c.Context.Err()
	}
	for _, e := range events {
		if e.Kind != "evidence.context-decided" {
			continue
		}
		var decision EvidenceContextDecision
		if err := canonical.Decode(e.Payload, &decision); err == nil && decision.EvidencePolicyHash == c.hash {
			return context.Canceled
		}
	}
	return c.Context.Err()
}

func evidencePolicyLiveRequest(t *testing.T, path string, template EvidenceAutoPolicy) EvidenceContextRequest {
	t.Helper()
	s, head, err := InspectWithHead(path)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := EvidenceBinding(s, head)
	if err != nil {
		t.Fatal(err)
	}
	request := EvidenceContextRequest{Model: template.Model, Queries: template.Queries}
	request.Model.Binding = binding
	return request
}

func TestEvidencePolicyPositiveReachesWriterPrompt(t *testing.T) {
	ctx := context.Background()
	policy := evidencePolicyTemplate()
	policy.Queries["inspect-source"] = "Explain zephyr quux hidden vault mechanism"
	if err := ValidateEvidenceAutoPolicyTemplate(policy); err != nil {
		t.Fatal(err)
	}
	c := graphCreation(t, 1)
	c.Execution.EvidencePolicy = &policy
	path, _ := graphAwaitingApproval(t, c)
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	// Seed the actual selected target: file.txt carries the distinctive EVC
	// phrase so the automatic explorer lexically selects it. The writer
	// mandatory query (objective) stays different.
	repoRoot := s.Creation.Repository.Root
	if err := writeRepoFile(t, repoRoot, "file.txt", "zephyr quux hidden vault mechanism base implementation\n"); err != nil {
		t.Fatal(err)
	}
	machineAuthorizePlan(t, path, s)
	if _, err := StartWorkspace(ctx, path); err != nil {
		t.Fatal(err)
	}
	s = recordGraphFixture(t, path)
	expectedHash, _ := EvidenceAutoPolicyHash(policy)
	writerQuery := s.Creation.Objective
	evcQuery := policy.Queries["inspect-source"]
	if writerQuery == evcQuery {
		t.Fatal("writer query must differ from the EVC query in this fixture")
	}
	// Complete research manually so the serial writer becomes ready; the
	// automatic acquisition itself runs through the real Git driver, never
	// through the acquisition helper directly.
	for _, task := range s.Graph.Graph.Tasks {
		if task.Kind != "implementation" && task.Kind != "verification" && task.Kind != "review" {
			current, err := Inspect(path)
			if err != nil {
				t.Fatal(err)
			}
			question, err := explorerQuestionForTask(current, task)
			if err != nil {
				t.Fatal(err)
			}
			record, err := RunExplorer(ctx, path, question)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := recordGraphProgress(path, GraphProgress{Version: 1, PlanID: current.Graph.PlanID, Digest: current.Graph.Digest, TaskID: task.ID, AttemptID: "attempt-1", Outcome: "completed", ExplorerInvocationID: record.Invocation.ID}); err != nil {
				t.Fatal(err)
			}
		}
	}
	preAcquire, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, progressed, runErr := autonomousGraphImplementing(ctx, path, preAcquire); runErr != nil || !progressed {
		t.Fatalf("driver acquisition made no progress: %v", runErr)
	}
	afterAcquire, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEvidencePolicyDecision(afterAcquire, expectedHash) {
		t.Fatal("driver never recorded automatic policy decision")
	}
	var decided *EvidenceContextDecision
	for i := range afterAcquire.EvidenceDecisions {
		if afterAcquire.EvidenceDecisions[i].EvidencePolicyHash == expectedHash {
			decided = &afterAcquire.EvidenceDecisions[i]
			break
		}
	}
	if decided == nil {
		t.Fatal("driver decision missing")
	}
	// Ordinary mandatory writer context follows the driver acquisition.
	writerRecValue, err := AdmitTaskContext(ctx, path, "writer", writerQuery)
	if err != nil {
		t.Fatal(err)
	}
	if writerRecValue.EvidenceDecisionID != "" {
		t.Fatal("ordinary writer context gained decision linkage")
	}
	writerRec, err := taskContextForRole(afterAcquire, "writer", writerQuery)
	if err != nil {
		// Re-inspect after manual admission.
		current, ierr := Inspect(path)
		if ierr != nil {
			t.Fatal(ierr)
		}
		var rerr error
		writerRec, rerr = taskContextForRole(current, "writer", writerQuery)
		if rerr != nil {
			t.Fatal(rerr)
		}
	}
	_ = writerRec
	if decided.Report.Selected != "inspect-source" {
		t.Fatalf("driver selected unexpected action: %q", decided.Report.Selected)
	}
	if decided.Request.Queries[decided.Report.Selected] != evcQuery {
		t.Fatal("selected query does not match seeded policy query")
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
	// Explorer admission bound to the decision must have selected file.txt.
	var explorerRec *TaskContextRecord
	for i := range snap.TaskContexts {
		rec := &snap.TaskContexts[i]
		if rec.EvidenceDecisionID == decided.ID && rec.Role == "explorer" {
			explorerRec = rec
			break
		}
	}
	if explorerRec == nil {
		t.Fatal("decision-bound explorer admission missing")
	}
	if !containsPath(selectedPaths(*explorerRec), "file.txt") {
		t.Fatalf("seeded file.txt not selected by EVC explorer: %v", selectedPaths(*explorerRec))
	}
	if writerRec == nil {
		t.Fatal("ordinary writer context missing after driver acquisition")
	}
	if writerRec.EvidenceDecisionID != "" {
		t.Fatal("ordinary writer context gained decision linkage")
	}
	if writerRec.SourceID != sourceID || writerRec.CandidateID != candidateID {
		t.Fatal("writer manifest source/candidate mismatch")
	}
	if !containsPath(selectedPaths(*writerRec), "file.txt") {
		t.Fatalf("writer did not reuse EVC hint for different query: %v", selectedPaths(*writerRec))
	}
	foundHint := false
	for _, sel := range writerRec.Manifest.Selected {
		if sel.Path == "file.txt" && sel.Reason == "path_hint" {
			foundHint = true
		}
	}
	if !foundHint {
		t.Fatalf("seeded file.txt lacks advisory hint reason: %+v", writerRec.Manifest.Selected)
	}
	if len(writerRec.Manifest.Selected) == 0 || len(writerRec.Manifest.Selected) > 12 || writerRec.Manifest.SelectedBytes > 48<<10 {
		t.Fatalf("writer selection left existing bounds: %+v", writerRec.Manifest)
	}
	// Journal order proves decision before writer admission.
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	decidedIdx, writerIdx := -1, -1
	for i, e := range events {
		if e.Kind == "evidence.context-decided" && decidedIdx < 0 {
			var d EvidenceContextDecision
			if canonical.Decode(e.Payload, &d) == nil && d.EvidencePolicyHash == expectedHash {
				decidedIdx = i
			}
		}
		if e.Kind == "task.context-admitted" && writerIdx < 0 {
			var r TaskContextRecord
			if canonical.Decode(e.Payload, &r) == nil && r.Role == "writer" && r.Query == writerRec.Query {
				writerIdx = i
			}
		}
	}
	if decidedIdx < 0 || writerIdx < 0 || decidedIdx > writerIdx {
		t.Fatal("policy decision not recorded before writer admission")
	}
	inv, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inv.Input, writerQuery) {
		t.Fatal("writer prompt lost mandatory objective")
	}
	var wm map[string]any
	if err := json.Unmarshal([]byte(inv.Input), &wm); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(wm["task_context"])
	var bound TaskContextRecord
	if err := canonical.Decode(raw, &bound); err != nil {
		t.Fatalf("writer task context is not canonical: %v", err)
	}
	if bound.ManifestID != writerRec.ManifestID {
		t.Fatal("writer prompt manifest differs from persisted record")
	}
	foundPromptHint := false
	for _, sel := range bound.Manifest.Selected {
		if sel.Path == "file.txt" && sel.Reason == "path_hint" {
			foundPromptHint = true
		}
	}
	if !foundPromptHint {
		t.Fatal("writer prompt lost hint-selected file.txt")
	}
}

func TestEvidencePolicyNoPositiveRetainsWriterContext(t *testing.T) {
	ctx := context.Background()
	for _, mutate := range []func(*EvidenceAutoPolicy){
		func(p *EvidenceAutoPolicy) { p.Model.Resources[0].Price = "1000000" },
		func(p *EvidenceAutoPolicy) { p.Model.Actions[0].Costs["local_compute_ms"] = nil },
	} {
		policy := evidencePolicyTemplate()
		mutate(&policy)
		if err := ValidateEvidenceAutoPolicyTemplate(policy); err != nil {
			t.Fatal("no-positive template rejected", err)
		}
		path, s := graphPolicyFixture(t, &policy)
		updated, err := maybeAcquireEvidencePolicy(ctx, path, s)
		if err != nil {
			t.Fatal(err)
		}
		expectedHash, _ := EvidenceAutoPolicyHash(policy)
		if !hasEvidencePolicyDecision(updated, expectedHash) {
			t.Fatal("no-positive decision not retained")
		}
		if updated.EvidenceDecisions[0].Report.Selected != "" {
			t.Fatal("ineligible model selected")
		}
		if len(updated.TaskContexts) != 0 {
			t.Fatal("no-positive acquisition read source")
		}
		if _, err := AdmitTaskContext(ctx, path, "writer", updated.Creation.Objective); err != nil {
			t.Fatal("mandatory writer context blocked", err)
		}
		if _, err := PrepareWriterInvocation(path); err != nil {
			t.Fatal("writer prompt unavailable after no-positive policy", err)
		}
	}
}

func TestEvidencePolicyOnlyOnceAcrossProgress(t *testing.T) {
	ctx := context.Background()
	policy := evidencePolicyTemplate()
	path, s := graphPolicyFixture(t, &policy)
	first, err := maybeAcquireEvidencePolicy(ctx, path, s)
	if err != nil {
		t.Fatal(err)
	}
	expectedHash, _ := EvidenceAutoPolicyHash(policy)
	if !hasEvidencePolicyDecision(first, expectedHash) {
		t.Fatal("first opportunity missed")
	}
	before, _ := journal.Read(path)
	// Repeated preparation/resume must not repeat acquisition.
	for i := 0; i < 3; i++ {
		latest, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		after, err := maybeAcquireEvidencePolicy(ctx, path, latest)
		if err != nil {
			t.Fatal(err)
		}
		_ = after
	}
	middle, _ := journal.Read(path)
	if len(middle) != len(before) {
		t.Fatal("repeated preparation repeated acquisition")
	}
	// Complete research/design so the serial implementation becomes active.
	researchSnap, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range researchSnap.Graph.Graph.Tasks {
		if task.Kind != "implementation" && task.Kind != "verification" && task.Kind != "review" {
			current, err := Inspect(path)
			if err != nil {
				t.Fatal(err)
			}
			// Skip already completed research (idempotent for reruns).
			completed := false
			if live, ierr := Inspect(path); ierr == nil && live.Graph != nil {
				if tsk, ok := live.Graph.Graph.Task(task.ID); ok && tsk.Completed {
					completed = true
				}
			}
			if completed {
				continue
			}
			question, err := explorerQuestionForTask(current, task)
			if err != nil {
				t.Fatal(err)
			}
			record, err := RunExplorer(ctx, path, question)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := recordGraphProgress(path, GraphProgress{Version: 1, PlanID: current.Graph.PlanID, Digest: current.Graph.Digest, TaskID: task.ID, AttemptID: "attempt-1", Outcome: "completed", ExplorerInvocationID: record.Invocation.ID}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Candidate advancement via a genuine CONFIRMED file effect plus graph
	// implementation progress must not reset the opportunity.
	if _, err := AdmitTaskContext(ctx, path, "writer", first.Creation.Objective); err != nil {
		t.Fatal(err)
	}
	beforeCandidate, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeID, err := beforeCandidate.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	confirmed := applyWriterOutputAfterContext(t, path, "writer output\n")
	if confirmed.FileOutcome != "CONFIRMED" {
		t.Fatalf("writer effect not confirmed: %s", confirmed.FileOutcome)
	}
	afterID, err := confirmed.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	if afterID == beforeID {
		t.Fatal("CONFIRMED writer effect did not advance candidate")
	}
	if err := recordGraphImplementationProgress(path); err != nil {
		t.Fatal(err)
	}
	advanced, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(advanced.EvidenceDecisions) != 1 || !hasEvidencePolicyDecision(advanced, expectedHash) {
		t.Fatal("candidate progress lost policy decision")
	}
	progressBaseline, _ := journal.Read(path)
	latest, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	afterAdvance, err := maybeAcquireEvidencePolicy(ctx, path, latest)
	if err != nil {
		t.Fatal(err)
	}
	_ = afterAdvance
	final, _ := journal.Read(path)
	if len(final) != len(progressBaseline) {
		t.Fatal("candidate progress reset the opportunity")
	}
	if countJournalKind(t, path, "evidence.context-decided") != 1 {
		t.Fatal("second automatic acquisition recorded")
	}
	// Operator-triggered matching acquisition never grants an extra opportunity.
	if _, err := AcquireEvidenceContext(ctx, path, evidencePolicyLiveRequest(t, path, policy)); err == nil {
		t.Fatal("operator matching acquisition granted extra opportunity")
	}
}

func TestEvidencePolicyGatesProduceNoReads(t *testing.T) {
	ctx := context.Background()
	t.Run("pending-pause-requested", func(t *testing.T) {
		policy := evidencePolicyTemplate()
		path, _ := graphPolicyFixture(t, &policy)
		if _, err := RequestPause(path, "operator", "pause-pending"); err != nil {
			t.Fatal(err)
		}
		before, _ := journal.Read(path)
		beforeRaw, _ := canonical.Bytes(before)
		snapBefore, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := maybeAcquireEvidencePolicy(ctx, path, snapBefore); err != nil {
			t.Fatal("pending gate propagated error", err)
		}
		after, _ := journal.Read(path)
		afterRaw, _ := canonical.Bytes(after)
		if !bytes.Equal(beforeRaw, afterRaw) {
			t.Fatal("pending gate produced journal writes")
		}
		snap, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.EvidenceDecisions) != 0 || len(snap.TaskContexts) != 0 {
			t.Fatal("pending gate acquired source evidence")
		}
	})
	t.Run("lifecycle-paused", func(t *testing.T) {
		policy := evidencePolicyTemplate()
		path, _ := graphPolicyFixture(t, &policy)
		if _, err := RequestPause(path, "operator", "pause-stop"); err != nil {
			t.Fatal(err)
		}
		if _, err := SettleLifecycle(path, "operator", "no stage was admitted", true); err != nil {
			t.Fatal(err)
		}
		snapBefore, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		if lifecycleStatus(snapBefore) != LifecyclePaused {
			t.Fatalf("fixture not paused: %s", lifecycleStatus(snapBefore))
		}
		before, _ := journal.Read(path)
		beforeRaw, _ := canonical.Bytes(before)
		if _, err := maybeAcquireEvidencePolicy(ctx, path, snapBefore); err != nil {
			t.Fatal("paused gate propagated error", err)
		}
		after, _ := journal.Read(path)
		afterRaw, _ := canonical.Bytes(after)
		if !bytes.Equal(beforeRaw, afterRaw) {
			t.Fatal("paused gate produced journal writes")
		}
		snap, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.EvidenceDecisions) != 0 || len(snap.TaskContexts) != 0 {
			t.Fatal("paused gate acquired source evidence")
		}
	})
	t.Run("graph-unknown", func(t *testing.T) {
		policy := evidencePolicyTemplate()
		path, _ := graphPolicyFixture(t, &policy)
		snap, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, task := range snap.Graph.Graph.Tasks {
			if task.Kind == engineeringplan.Research || task.Kind == engineeringplan.Design {
				question, err := explorerQuestionForTask(snap, task)
				if err != nil {
					t.Fatal(err)
				}
				record, err := RunExplorer(ctx, path, question)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := recordGraphProgress(path, GraphProgress{Version: 1, PlanID: snap.Graph.PlanID, Digest: snap.Graph.Digest, TaskID: task.ID, AttemptID: "attempt-1", Outcome: "unknown", ExplorerInvocationID: record.Invocation.ID}); err != nil {
					t.Fatal(err)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatal("no research task for UNKNOWN fixture")
		}
		unknownSnap, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		if !graphHasUnknown(unknownSnap) {
			t.Fatal("fixture did not record UNKNOWN")
		}
		before, _ := journal.Read(path)
		beforeRaw, _ := canonical.Bytes(before)
		beforeDecisions := len(unknownSnap.EvidenceDecisions)
		beforeContexts := len(unknownSnap.TaskContexts)
		if _, err := maybeAcquireEvidencePolicy(ctx, path, unknownSnap); err != nil {
			t.Fatal("UNKNOWN gate propagated error", err)
		}
		after, _ := journal.Read(path)
		afterRaw, _ := canonical.Bytes(after)
		if !bytes.Equal(beforeRaw, afterRaw) {
			t.Fatal("UNKNOWN gate produced journal writes")
		}
		snapAfter, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapAfter.EvidenceDecisions) != beforeDecisions || len(snapAfter.TaskContexts) != beforeContexts {
			t.Fatal("UNKNOWN gate acquired source evidence")
		}
	})
	t.Run("ready", func(t *testing.T) {
		policy := evidencePolicyTemplate()
		c := graphCreation(t, 1)
		c.Execution.EvidencePolicy = &policy
		path, _ := graphAwaitingApproval(t, c)
		s, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		machineAuthorizePlan(t, path, s)
		if _, err := StartWorkspace(ctx, path); err != nil {
			t.Fatal(err)
		}
		_ = recordGraphFixture(t, path)
		// Build a real READY journal via manual research, driver
		// acquisition and native writer/verification/review (fixture scope:
		// fake explorer/writer/reviewer plus native git verification).
		snapResearch, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range snapResearch.Graph.Graph.Tasks {
			if task.Kind != engineeringplan.Research && task.Kind != engineeringplan.Design {
				continue
			}
			current, err := Inspect(path)
			if err != nil {
				t.Fatal(err)
			}
			question, err := explorerQuestionForTask(current, task)
			if err != nil {
				t.Fatal(err)
			}
			record, err := RunExplorer(ctx, path, question)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := recordGraphProgress(path, GraphProgress{Version: 1, PlanID: current.Graph.PlanID, Digest: current.Graph.Digest, TaskID: task.ID, AttemptID: "attempt-1", Outcome: "completed", ExplorerInvocationID: record.Invocation.ID}); err != nil {
				t.Fatal(err)
			}
		}
		preDriver, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, progressed, runErr := autonomousGraphImplementing(ctx, path, preDriver); runErr != nil || !progressed {
			t.Fatalf("READY fixture driver acquisition failed: %v", runErr)
		}
		driverSnap, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := AdmitTaskContext(ctx, path, "writer", driverSnap.Creation.Objective); err != nil {
			t.Fatal(err)
		}
		_ = applyWriterOutputAfterContext(t, path, "writer output\n")
		if err := recordGraphImplementationProgress(path); err != nil {
			t.Fatal(err)
		}
		verified, err := Verify(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		_ = verified
		if err := recordGraphVerificationProgress(path); err != nil {
			t.Fatal(err)
		}
		reviewed, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := AdmitTaskContext(ctx, path, "reviewer", reviewed.Creation.Objective); err != nil {
			t.Fatal(err)
		}
		invocation, err := PrepareReviewInvocation(path)
		if err != nil {
			t.Fatal(err)
		}
		candidateID, err := reviewed.Candidate.ID()
		if err != nil {
			t.Fatal(err)
		}
		verdict := ReviewVerdict{CandidateID: candidateID, VerificationPlanID: reviewed.Verification.PlanID, Decision: "approve", Findings: []ReviewFinding{}}
		output, err := canonical.Bytes(verdict)
		if err != nil {
			t.Fatal(err)
		}
		model := invocation.Profile.Model
		if _, err := RecordReview(path, ReviewRecord{Invocation: invocation, Result: runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, Output: string(output)}}); err != nil {
			t.Fatal(err)
		}
		if err := recordGraphReviewProgress(path); err != nil {
			t.Fatal(err)
		}
		ready, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		if ready.State != "READY" {
			t.Fatalf("READY fixture not ready: %s", ready.State)
		}
		before, _ := journal.Read(path)
		beforeRaw, _ := canonical.Bytes(before)
		beforeDecisions := len(ready.EvidenceDecisions)
		beforeContexts := len(ready.TaskContexts)
		if _, err := maybeAcquireEvidencePolicy(ctx, path, ready); err != nil {
			t.Fatal("READY gate propagated error", err)
		}
		after, _ := journal.Read(path)
		afterRaw, _ := canonical.Bytes(after)
		if !bytes.Equal(beforeRaw, afterRaw) {
			t.Fatal("READY gate produced journal writes")
		}
		snap, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.EvidenceDecisions) != beforeDecisions || len(snap.TaskContexts) != beforeContexts {
			t.Fatal("READY gate acquired source evidence")
		}
	})
}

func TestEvidencePolicyPrepareOnlyDispatchesNoModels(t *testing.T) {
	path, _ := graphPolicyFixture(t, func() *EvidenceAutoPolicy { p := evidencePolicyTemplate(); return &p }())
	prepared, err := PrepareAutonomous(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.EvidenceDecisions) != 0 || len(prepared.TaskContexts) != 0 {
		t.Fatal("prepare-only performed automatic acquisition")
	}
	if len(prepared.ModelAccess) != 0 || len(prepared.Explorations) != 0 {
		t.Fatal("prepare-only dispatched models")
	}
}

func TestEvidencePolicyFullGraphReachesNativeReviewReady(t *testing.T) {
	// Fixture scope: real Git worktree with fake explorer/writer/reviewer
	// runtimes and native `git --version` verification to READY. No provider
	// calls occur for the optional acquisition; live quality remains NOT RUN.
	ctx := context.Background()
	policy := evidencePolicyTemplate()
	policy.Queries["inspect-source"] = "Explain zephyr quux hidden vault mechanism"
	if err := ValidateEvidenceAutoPolicyTemplate(policy); err != nil {
		t.Fatal(err)
	}
	c := graphCreation(t, 1)
	c.Execution.EvidencePolicy = &policy
	path, _ := graphAwaitingApproval(t, c)
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRepoFile(t, s.Creation.Repository.Root, "file.txt", "zephyr quux hidden vault mechanism base implementation\n"); err != nil {
		t.Fatal(err)
	}
	machineAuthorizePlan(t, path, s)
	if _, err := StartWorkspace(ctx, path); err != nil {
		t.Fatal(err)
	}
	_ = recordGraphFixture(t, path)
	expectedHash, _ := EvidenceAutoPolicyHash(policy)
	// Real driver integration for acquisition: never call the acquisition
	// helper directly. Research completes manually, then a single
	// autonomousGraphImplementing step records the automatic decision.
	snapResearch, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range snapResearch.Graph.Graph.Tasks {
		if task.Kind != engineeringplan.Research && task.Kind != engineeringplan.Design {
			continue
		}
		current, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		question, err := explorerQuestionForTask(current, task)
		if err != nil {
			t.Fatal(err)
		}
		record, err := RunExplorer(ctx, path, question)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := recordGraphProgress(path, GraphProgress{Version: 1, PlanID: current.Graph.PlanID, Digest: current.Graph.Digest, TaskID: task.ID, AttemptID: "attempt-1", Outcome: "completed", ExplorerInvocationID: record.Invocation.ID}); err != nil {
			t.Fatal(err)
		}
	}
	preDriver, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, progressed, runErr := autonomousGraphImplementing(ctx, path, preDriver); runErr != nil || !progressed {
		t.Fatalf("driver acquisition made no progress: %v", runErr)
	}
	driverSnap, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEvidencePolicyDecision(driverSnap, expectedHash) || len(driverSnap.EvidenceDecisions) != 1 {
		t.Fatal("driver acquisition missed before writer")
	}
	decided := driverSnap.EvidenceDecisions[0]
	if decided.Report.Selected != "inspect-source" {
		t.Fatalf("READY decision selected unexpected action: %q", decided.Report.Selected)
	}
	// Ordinary mandatory writer context follows the driver acquisition.
	// Hint proof uses the pre-apply writer manifest (candidate advances on
	// CONFIRMED), then a confirmed writer effect, native verification and
	// independent review reach READY.
	writerQueryPre := driverSnap.Creation.Objective
	if writerQueryPre == policy.Queries["inspect-source"] {
		t.Fatal("writer query must differ from EVC query")
	}
	if _, err := AdmitTaskContext(ctx, path, "writer", writerQueryPre); err != nil {
		t.Fatal(err)
	}
	writerSnap, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	wrecPre, err := taskContextForRole(writerSnap, "writer", writerQueryPre)
	if err != nil {
		t.Fatal("persisted writer manifest missing", err)
	}
	if !containsPath(selectedPaths(*wrecPre), "file.txt") {
		t.Fatalf("writer manifest missed seeded file.txt: %v", selectedPaths(*wrecPre))
	}
	foundPreHint := false
	for _, sel := range wrecPre.Manifest.Selected {
		if sel.Path == "file.txt" && sel.Reason == "path_hint" {
			foundPreHint = true
		}
	}
	if !foundPreHint {
		t.Fatalf("writer manifest lacks path_hint for file.txt: %+v", wrecPre.Manifest.Selected)
	}
	if len(wrecPre.Manifest.Selected) == 0 || len(wrecPre.Manifest.Selected) > 12 || wrecPre.Manifest.SelectedBytes > 48<<10 {
		t.Fatalf("writer selection left existing bounds: %+v", wrecPre.Manifest)
	}
	winvPre, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	var wmPre map[string]any
	if err := json.Unmarshal([]byte(winvPre.Input), &wmPre); err != nil {
		t.Fatal(err)
	}
	rawPre, _ := json.Marshal(wmPre["task_context"])
	var boundPre TaskContextRecord
	if err := canonical.Decode(rawPre, &boundPre); err != nil {
		t.Fatalf("writer prompt context is not canonical: %v", err)
	}
	if boundPre.ManifestID != wrecPre.ManifestID {
		t.Fatal("writer prompt manifest differs from persisted record")
	}
	confirmed := applyWriterOutputAfterContext(t, path, "writer output\n")
	_ = confirmed
	if err := recordGraphImplementationProgress(path); err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if verified.State != "REVIEWING" || verified.Verification == nil {
		t.Fatalf("native verification did not reach review: %s", verified.State)
	}
	if err := recordGraphVerificationProgress(path); err != nil {
		t.Fatal(err)
	}
	reviewed, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitTaskContext(ctx, path, "reviewer", reviewed.Creation.Objective); err != nil {
		t.Fatal(err)
	}
	invocation, err := PrepareReviewInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	candidateID, err := reviewed.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	verdict := ReviewVerdict{CandidateID: candidateID, VerificationPlanID: reviewed.Verification.PlanID, Decision: "approve", Findings: []ReviewFinding{}}
	output, err := canonical.Bytes(verdict)
	if err != nil {
		t.Fatal(err)
	}
	model := invocation.Profile.Model
	if _, err := RecordReview(path, ReviewRecord{Invocation: invocation, Result: runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, Output: string(output)}}); err != nil {
		t.Fatal(err)
	}
	if err := recordGraphReviewProgress(path); err != nil {
		t.Fatal(err)
	}
	final, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != "READY" {
		t.Fatalf("policy run did not reach READY: %s", final.State)
	}
	if err := requireGraphReady(final); err != nil {
		t.Fatal("READY lacks completed graph evidence", err)
	}
	if !hasEvidencePolicyDecision(final, expectedHash) || len(final.EvidenceDecisions) != 1 {
		t.Fatal("READY lost the single policy decision")
	}
	if final.Verification == nil || final.Review == nil {
		t.Fatal("READY lacks native verification/review evidence")
	}
	// Decision must precede writer admission in the durable journal.
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	decidedIdx, writerIdx := -1, -1
	for i, e := range events {
		if e.Kind == "evidence.context-decided" && decidedIdx < 0 {
			var d EvidenceContextDecision
			if canonical.Decode(e.Payload, &d) == nil && d.EvidencePolicyHash == expectedHash {
				decidedIdx = i
			}
		}
		if e.Kind == "task.context-admitted" && writerIdx < 0 {
			var r TaskContextRecord
			if canonical.Decode(e.Payload, &r) == nil && r.Role == "writer" {
				writerIdx = i
			}
		}
	}
	if decidedIdx < 0 || writerIdx < 0 || decidedIdx > writerIdx {
		t.Fatal("policy decision not recorded before writer admission")
	}
	// Final READY retains the single driver decision; pre-apply hint proof
	// above already established exact candidate/source path_hint within
	// bounds for the seeded file.txt with a different writer query.
	if writerQueryPre == policy.Queries["inspect-source"] {
		t.Fatal("writer query must differ from EVC query")
	}
}

func applyWriterOutputAfterContext(t *testing.T, path, after string) Snapshot {
	t.Helper()
	rec := recordAutonomousProposal(t, path, after)
	live, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := autonomousFileAuthorization(live, rec.Prepared.Intent)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := ApplyFiles(context.Background(), path, rec.Prepared, a)
	if err != nil || confirmed.FileOutcome != "CONFIRMED" {
		t.Fatal(err, confirmed.FileOutcome)
	}
	return confirmed
}
