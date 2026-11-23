package control

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/evidencevalue"
	"github.com/iancuuandrei/fabric/internal/journal"
)

func evidenceContextFixtureRequest(t *testing.T, path string) EvidenceContextRequest {
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
	model := evidencevalue.Request{Version: 1, Binding: binding, Resources: []evidencevalue.Resource{{Name: "local_compute_ms", Limit: 100, Price: 1}}, Actions: []evidencevalue.Action{{ID: "inspect-source", Kind: "source_read", Reliability: evidencevalue.Reliability{Ordinal: 2}, Outcomes: []evidencevalue.Outcome{{Probability: .5, Utilities: []float64{1, 0}}, {Probability: .5, Utilities: []float64{0, 1}}}, Costs: map[string]*float64{"local_compute_ms": &cost}}}}
	return EvidenceContextRequest{Model: evidencevalue.EncodeRequest(model), Queries: map[string]string{"inspect-source": "Explain file.txt boundary behavior"}}
}

func TestEvidenceContextAcquirePersistsDecisionAndFeedsExplorer(t *testing.T) {
	path, _ := boundedTaskFixture(t, map[string]string{"file.txt": "boundary observation retained\n"})
	request := evidenceContextFixtureRequest(t, path)
	result, err := AcquireEvidenceContext(context.Background(), path, request)
	if err != nil || result.Context == nil || result.Decision.Report.Selected != "inspect-source" {
		t.Fatal("positive decision did not acquire", result, err)
	}
	snapshot, err := Inspect(path)
	if err != nil || len(snapshot.EvidenceDecisions) != 1 || len(snapshot.TaskContexts) != 1 || snapshot.EvidenceDecisions[0].ID != result.Decision.ID {
		t.Fatal("derived evidence did not replay", err)
	}
	input, err := PrepareExplorerInvocation(path, request.Queries["inspect-source"])
	if err != nil || !strings.Contains(input.Input, "boundary observation retained") {
		t.Fatal("acquired evidence did not reach role input", err)
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	index := -1
	for i, event := range events {
		if event.Kind == "evidence.context-decided" {
			index = i
			break
		}
	}
	if index < 0 || events[index].Previous != request.Model.Binding.JournalHead {
		t.Fatal("decision lacks exact producing prefix")
	}
	// Replay rejects substituted selection/targets even with otherwise valid
	// canonical payloads. Neither journal hash nor a positive score is authority.
	for _, change := range []func(*EvidenceContextDecision){
		func(d *EvidenceContextDecision) { d.Report.Selected = "other" },
		func(d *EvidenceContextDecision) { d.Request.Queries["inspect-source"] = "different question" },
		func(d *EvidenceContextDecision) { d.ID = strings.Repeat("a", 64) },
		func(d *EvidenceContextDecision) { d.Request.Model.Binding.JournalHead = strings.Repeat("a", 64) },
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
			t.Fatal("substituted decision accepted")
		}
	}
	before, _ := journal.Read(path)
	if err := Append(path, "evidence.context-decided", result.Decision); err == nil {
		t.Fatal("stale decision appended against newer locked prefix")
	}
	if _, err := AcquireEvidenceContext(context.Background(), path, request); err == nil {
		t.Fatal("stale request accepted")
	}
	after, _ := journal.Read(path)
	if len(after) != len(before) {
		t.Fatal("stale input appended state")
	}
}

func TestEvidenceContextDecisionGapRejectsChangedStateOnAdmissionReplay(t *testing.T) {
	path, _ := boundedTaskFixture(t, map[string]string{"file.txt": "bound source\n"})
	request := evidenceContextFixtureRequest(t, path)
	result, err := AcquireEvidenceContext(context.Background(), path, request)
	if err != nil || result.Context == nil || result.Context.EvidenceDecisionID != result.Decision.ID {
		t.Fatal("missing decision-linked admission", err)
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	index := len(events) - 1
	if events[index].Kind != "task.context-admitted" {
		t.Fatal("expected final source admission")
	}
	prefix, err := Replay(events[:index])
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Snapshot){
		func(s *Snapshot) { s.State = "READY" },
		func(s *Snapshot) { s.WorkspaceOutcome = "UNKNOWN" },
		func(s *Snapshot) { s.Lifecycle.Status = LifecyclePaused },
		func(s *Snapshot) { s.RunID = strings.Repeat("e", 64) },
		func(s *Snapshot) {
			candidate := *s.Candidate
			candidate.FilesHash = strings.Repeat("e", 64)
			s.Candidate = &candidate
		},
		func(s *Snapshot) { s.EvidenceDecisions = nil },
	} {
		changed := prefix
		change(&changed)
		if requireEvidenceContextBinding(changed, "explorer", request.Queries["inspect-source"], &result.Decision) == nil {
			t.Fatal("changed state allowed source read")
		}
		if replayTaskContext(&changed, events[index]) == nil {
			t.Fatal("changed state allowed source admission replay")
		}
	}
}

func TestEvidenceContextStopsWithoutAcquiringSource(t *testing.T) {
	path, _ := boundedTaskFixture(t, map[string]string{"file.txt": "must not be acquired\n"})
	for _, unavailable := range []bool{false, true} {
		request := evidenceContextFixtureRequest(t, path)
		if unavailable {
			request.Model.Actions[0].Costs["local_compute_ms"] = nil
		} else {
			request.Model.Resources[0].Price = "100"
		}
		result, err := AcquireEvidenceContext(context.Background(), path, request)
		if err != nil || result.Context != nil || result.Decision.Report.Selected != "" {
			t.Fatal("ineligible decision acquired source", err)
		}
	}
	s, err := Inspect(path)
	if err != nil || len(s.TaskContexts) != 0 || len(s.EvidenceDecisions) != 2 {
		t.Fatal("stopping failed to preserve decision-only evidence", err)
	}
}

func TestEvidenceContextRejectsMalformedTargetsWithoutJournalMutation(t *testing.T) {
	path, _ := boundedTaskFixture(t, map[string]string{"file.txt": "data\n"})
	request := evidenceContextFixtureRequest(t, path)
	before, _ := journal.Read(path)
	for _, change := range []func(*EvidenceContextRequest){
		func(r *EvidenceContextRequest) { r.Model.Actions[0].Kind = "spawn_explorer" },
		func(r *EvidenceContextRequest) { delete(r.Queries, "inspect-source") },
		func(r *EvidenceContextRequest) { r.Queries["extra"] = "not bound" },
		func(r *EvidenceContextRequest) {
			r.Queries["inspect-source"] = strings.Repeat("x", taskContextQueryCap+1)
		},
	} {
		raw, _ := canonical.Bytes(request)
		var changed EvidenceContextRequest
		if err := json.Unmarshal(raw, &changed); err != nil {
			t.Fatal(err)
		}
		change(&changed)
		if _, err := AcquireEvidenceContext(context.Background(), path, changed); err == nil {
			t.Fatal("malformed frontier accepted")
		}
	}
	after, _ := journal.Read(path)
	a, _ := canonical.Bytes(before)
	b, _ := canonical.Bytes(after)
	if !bytes.Equal(a, b) {
		t.Fatal("rejection changed journal")
	}
}

func TestEvidenceContextControllerStopsOverridePositiveModel(t *testing.T) {
	path, s := boundedTaskFixture(t, map[string]string{"file.txt": "data\n"})
	request := evidenceContextFixtureRequest(t, path)
	for _, change := range []func(*Snapshot){
		func(s *Snapshot) { s.State = "READY" },
		func(s *Snapshot) { s.State = "READY"; s.WorkspaceOutcome = "UNKNOWN" },
		func(s *Snapshot) { s.Lifecycle.Status = LifecyclePaused },
	} {
		stopped := s
		change(&stopped)
		decision, err := evidenceContextDecision(stopped, request.Model.Binding.JournalHead, request)
		if err != nil || decision.Report.Selected != "" {
			t.Fatal("controller stop did not override positive estimate", err)
		}
		if stopped.WorkspaceOutcome == "UNKNOWN" && decision.Report.StopReason != "effect_requires_reconciliation" {
			t.Fatal("UNKNOWN lost precedence")
		}
	}
}
