package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/evidencevalue"
)

func checkEvidenceValueCLI(t *testing.T, s control.Snapshot, run func(...string) []byte) {
	t.Helper()
	root := s.Creation.Repository.Root
	path, err := runPath(root, s.RunID)
	if err != nil {
		t.Fatal(err)
	}
	_, head, err := control.InspectWithHead(path)
	if err != nil {
		t.Fatal(err)
	}
	source, _ := s.Creation.Repository.ID()
	candidate, _ := s.Candidate.ID()
	cost := 1.0
	q := evidencevalue.Request{Version: 1, Binding: evidencevalue.Binding{RunID: s.RunID, SourceID: source, CandidateID: candidate, JournalHead: head}, Resources: []evidencevalue.Resource{{Name: "wall_ms", Limit: 100}}, Actions: []evidencevalue.Action{{ID: "read", Kind: "source_read", Reliability: evidencevalue.Reliability{Ordinal: 2}, Costs: map[string]*float64{"wall_ms": &cost}, Outcomes: []evidencevalue.Outcome{{Probability: .5, Utilities: []float64{1, 0}}, {Probability: .5, Utilities: []float64{0, 1}}}}}}
	file := filepath.Join(t.TempDir(), "evidence.json")
	write := func() {
		t.Helper()
		raw, err := canonical.Bytes(evidencevalue.EncodeRequest(q))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	before := run("inspect", s.RunID, "--export-jsonl")
	var report evidencevalue.WireReport
	if err := json.Unmarshal(run("evidence-value", s.RunID, file), &report); err != nil || report.Selected != "read" || report.Binding != q.Binding || report.Authority != "advisory_only_requires_controller_admission" {
		t.Fatal(report, err)
	}
	if !bytes.Equal(before, run("inspect", s.RunID, "--export-jsonl")) {
		t.Fatal("recommendation changed journal")
	}
	if report.RequestHash == "" {
		t.Fatal("decision omitted model identity")
	}
	var template evidencevalue.WireRequest
	if err := canonical.Decode(run("evidence-value", s.RunID, "--template"), &template); err != nil || template.Binding != q.Binding || len(template.Actions) != 0 || len(template.Resources) != 0 {
		t.Fatal("template binding changed", err)
	}
	for _, field := range []string{"head", "candidate", "source", "run"} {
		old := q.Binding
		switch field {
		case "head":
			q.Binding.JournalHead = ""
		case "candidate":
			q.Binding.CandidateID = ""
		case "source":
			q.Binding.SourceID = ""
		case "run":
			q.Binding.RunID = ""
		}
		write()
		var b bytes.Buffer
		if err := evidenceValueCommand(root, []string{s.RunID, file}, &b); err == nil || b.Len() != 0 {
			t.Fatal("accepted detached model", field)
		}
		q.Binding = old
	}
	if !bytes.Equal(before, run("inspect", s.RunID, "--export-jsonl")) {
		t.Fatal("rejection changed journal")
	}
	// Legacy runs have no bounded context policy. The new acquisition command
	// must fail closed before a decision write or source acquisition.
	q.Actions[0].Kind = "source_read"
	acquisition := control.EvidenceContextRequest{Model: evidencevalue.EncodeRequest(q), Queries: map[string]string{"read": "Explain source.txt"}}
	raw, err := canonical.Bytes(acquisition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var denied bytes.Buffer
	if err := Execute(context.Background(), []string{"evidence-acquire", s.RunID, file}, root, &denied); err == nil || denied.Len() != 0 {
		t.Fatal("legacy policy admitted optional acquisition")
	}
	if !bytes.Equal(before, run("inspect", s.RunID, "--export-jsonl")) {
		t.Fatal("legacy acquisition rejection changed journal")
	}
	feedback := evidencevalue.FeedbackRequest{Model: evidencevalue.EncodeRequest(q), Allocations: map[string]evidencevalue.Numeric{"wall_ms": "100"}, Consumed: map[string][]string{}}
	raw, err = canonical.Bytes(feedback)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var priced control.EvidenceFeedbackReport
	if err := canonical.Decode(run("evidence-feedback", s.RunID, file), &priced); err != nil || priced.UsageHash == "" || len(priced.Observations) != 0 || len(priced.Feedback.Request.Consumed["wall_ms"]) != 0 {
		t.Fatal("fake work acquired observed costs", err)
	}
	if priced.Scope != "receipt_matched_completed_codex_typed_tokens" {
		t.Fatal("Codex-only feedback must keep the legacy scope", priced.Scope)
	}
	if priced.Feedback.Request.Model.Resources[0] != feedback.Model.Resources[0] {
		t.Fatal("unknown cost changed baseline")
	}
	if !bytes.Equal(before, run("inspect", s.RunID, "--export-jsonl")) {
		t.Fatal("resource feedback changed journal")
	}
	feedback.Model.Binding.JournalHead = ""
	raw, _ = canonical.Bytes(feedback)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	denied.Reset()
	if err := Execute(context.Background(), []string{"evidence-feedback", s.RunID, file}, root, &denied); err == nil || denied.Len() != 0 {
		t.Fatal("detached resource feedback accepted")
	}
}

func TestEvidenceAcquisitionWireIsStrict(t *testing.T) {
	for _, raw := range []string{
		`{"model":{},"queries":{},"permissions":"all"}`,
		`{"model":{},"queries":{},"queries":{}}`,
		`{"model":{"version":1,"authority":"approved"},"queries":{}}`,
	} {
		var request control.EvidenceContextRequest
		if canonical.Decode([]byte(raw), &request) == nil {
			t.Fatal("unsafe acquisition wire accepted")
		}
	}
}

func TestEvidenceValueInputBounds(t *testing.T) {
	file := filepath.Join(t.TempDir(), "oversized.json")
	if err := os.WriteFile(file, bytes.Repeat([]byte(" "), evidencevalue.MaxBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRegularPolicyJSON(file, evidencevalue.MaxBytes, "evidence request", "64 KiB"); err == nil {
		t.Fatal("accepted oversized file")
	}
}

func TestEvidenceValueControllerStops(t *testing.T) {
	for _, tc := range []struct {
		s      control.Snapshot
		reason string
	}{
		{control.Snapshot{State: "READY", WorkspaceOutcome: "UNKNOWN"}, "effect_requires_reconciliation"},
		{control.Snapshot{State: "READY"}, "accepted_checkpoint_requires_no_additional_research"},
	} {
		r := evidencevalue.Report{Selected: "read"}
		applyEvidenceControllerStop(tc.s, &r)
		if r.Selected != "" || r.StopReason != tc.reason {
			t.Fatal(r, tc.reason)
		}
	}
}
