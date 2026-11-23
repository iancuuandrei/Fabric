package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
)

func TestPreviousRepairAnchorsBindRunAndBoundFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	raw, err := canonical.Bytes(control.RepairDiagnosis{Version: 1, RunID: "run", Findings: []control.RepairFinding{}, Specifications: []control.RepairSpecification{}, Anchors: []control.RepairAnchor{{ID: "anchor", FindingID: "finding"}}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded control.RepairDiagnosis
	if err := canonical.Decode(raw, &decoded); err != nil {
		t.Fatal("fixture report decode", err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	anchors, err := previousRepairAnchors(path, "run")
	if err != nil || len(anchors) != 1 {
		t.Fatal("valid report not read", err)
	}
	if _, err := previousRepairAnchors(path, "different"); err == nil {
		t.Fatal("foreign run report accepted")
	}
	decoded.CandidateID = "different-candidate"
	mismatched, err := canonical.Bytes(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, mismatched, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := previousRepairAnchors(path, "run"); err == nil {
		t.Fatal("anchor/header candidate substitution accepted")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", (1<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := previousRepairAnchors(path, "run"); err == nil {
		t.Fatal("oversized report accepted")
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"run_id":"run","unknown":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := previousRepairAnchors(path, "run"); err == nil {
		t.Fatal("unknown report fields accepted")
	}
}

func TestDiagnoseArgumentsRejectBeforeJournalAccess(t *testing.T) {
	for _, args := range [][]string{nil, {"run", "--previous", "report.json"}, {"run", "--unknown"}, {"run", "--anchor", "extra"}, {"run", "--closure", "--anchor"}, {"run", "--closure", "--previous", "report.json"}, {"run", "--spectrum", "s.json", "--anchor"}, {"run", "--spectrum", "s.json", "--closure"}, {"run", "--spectrum", "s.json", "--previous", "r.json"}} {
		if err := diagnoseCommand(context.Background(), t.TempDir(), args, os.Stdout); err == nil {
			t.Fatalf("invalid diagnosis accepted %v", args)
		}
	}
}

func TestRepairContextArgumentsRejectBeforeJournalAccess(t *testing.T) {
	for _, args := range [][]string{nil, {"run"}, {"run", "s.json", "extra"}, {"run", "s.json", "--unknown"}, {"run", "s.json", "--task"}} {
		if err := repairContextCommand(context.Background(), t.TempDir(), args, os.Stdout); err == nil {
			t.Fatalf("invalid repair context arguments accepted %v", args)
		}
	}
}

func TestRepairSpectrumImportBoundsAndStrictEncoding(t *testing.T) {
	p := filepath.Join(t.TempDir(), "spectrum.json")
	for _, raw := range []string{`{"version":1,"unknown":true}`, `{"version":1,"version":1}`, strings.Repeat("x", (1<<20)+1), `{"version":1} {}`} {
		if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readRepairSpectrum(p); err == nil {
			t.Fatal("invalid spectrum file accepted")
		}
	}
	if err := os.WriteFile(p, []byte(`{"version":1,"run_id":"r","candidate_id":"c","sources":[],"tests":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := readRepairSpectrum(p)
	if err != nil || s.RunID != "r" {
		t.Fatal("valid shape not decoded", err)
	}
}

func TestReferenceEscapesAlternativeFlags(t *testing.T) {
	r := Reference()
	if !strings.Contains(r, `\| --spectrum SPECTRUM_JSON`) || !strings.Contains(r, `ri definition\|references`) {
		t.Fatal("argument alternatives broke generated Markdown table")
	}
}
