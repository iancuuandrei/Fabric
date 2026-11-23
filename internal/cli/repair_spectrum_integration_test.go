package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/faultlocalization"
)

// This fixture validates CLI/journal/source binding. Real Go-produced profiles
// are exercised independently in faultlocalization's native integration test.
func checkRepairSpectrumCLI(t *testing.T, s control.Snapshot, run func(...string) []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.Workspace.Request.Path, "source.txt"))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	candidate, _ := s.Candidate.ID()
	bundle := faultlocalization.Spectrum{Version: 1, RunID: s.RunID, CandidateID: candidate,
		Sources: []faultlocalization.Source{{Path: "source.txt", ProfilePath: "fixture/source.txt", SHA256: hex.EncodeToString(hash[:])}},
		Tests:   []faultlocalization.Test{{ID: "failed", Outcome: "FAIL", Profile: "mode: set\nfixture/source.txt:1.1,1.8 1 1\n"}, {ID: "passed", Outcome: "PASS", Profile: "mode: set\nfixture/source.txt:1.1,1.8 1 0\n"}}}
	raw, err := canonical.Bytes(bundle)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "spectrum.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	before := run("inspect", s.RunID, "--export-jsonl")
	var report control.RepairDiagnosis
	if err := json.Unmarshal(run("diagnose", s.RunID, "--spectrum", file), &report); err != nil {
		t.Fatal(err)
	}
	if report.Spectrum == nil || report.Spectrum.SourceStatus != "complete_candidate_bytes_observed" || len(report.Spectrum.Blocks) != 1 || report.Spectrum.Provenance != "caller_supplied_untrusted_test_spectra" {
		t.Fatal("CLI did not publish correctly scoped ranking", report)
	}
	if !bytes.Equal(before, run("inspect", s.RunID, "--export-jsonl")) {
		t.Fatal("spectrum inspection changed durable state")
	}
	var base control.RepairDiagnosis
	if err := json.Unmarshal(run("diagnose", s.RunID), &base); err != nil || base.Spectrum != nil {
		t.Fatal("optional spectrum leaked into ordinary diagnosis", err)
	}
}
