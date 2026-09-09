package control

import (
	"bytes"
	"context"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/ri"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLexicalFailureRetainsUnknownAndBlocksRetries(t *testing.T) {
	path, s := approvedRepository(t)
	observed, err := ri.ObserveLexical(context.Background(), s.Creation.Repository)
	if err != nil {
		t.Fatal(err)
	}
	manifestID, err := observed.Manifest.ID()
	if err != nil {
		t.Fatal(err)
	}
	plan := ri.LexicalPlan{Version: 1, Repository: s.Creation.Repository, ManifestID: manifestID, Files: len(observed.Manifest.Files), Executable: filepath.Join(t.TempDir(), "missing.exe"), ExecutableSHA256: strings.Repeat("a", 64), StageRoot: filepath.Join(t.TempDir(), "stage"), BatchBytes: 1048576, BatchFiles: 100}
	intent, err := plan.Intent(s.RunID, s.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteRILexical(context.Background(), path, plan, observed.Manifest, effects.Authorization{IntentID: strings.Repeat("b", 64), Actor: "operator"}); err == nil {
		t.Fatal("wrong authorization admitted")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected authorization changed journal", err)
	}
	failed, err := ExecuteRILexical(context.Background(), path, plan, observed.Manifest, effects.Authorization{IntentID: id, Actor: "operator"})
	if err == nil || failed.RILexical == nil || failed.RILexical.Outcome != "UNKNOWN" || failed.RILexical.Observation == nil || failed.RILexical.Observation.Build != nil {
		t.Fatal("failed staging lost UNKNOWN", err)
	}
	if _, err := os.Stat(plan.StageRoot); !os.IsNotExist(err) {
		t.Fatal("missing executable created staging", err)
	}
	failedJournal, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unresolved, err := ReconcileRILexical(context.Background(), path)
	if err == nil || unresolved.RILexical == nil || unresolved.RILexical.Outcome != "UNKNOWN" {
		t.Fatal("missing staging was confirmed by recovery", err)
	}
	if _, err := os.Stat(plan.StageRoot); !os.IsNotExist(err) {
		t.Fatal("recovery created staging", err)
	}
	if _, err := ExecuteRILexical(context.Background(), path, plan, observed.Manifest, effects.Authorization{IntentID: id, Actor: "operator"}); err == nil {
		t.Fatal("UNKNOWN staging retried")
	}
	if err := Append(path, "verification.started", struct{}{}); err == nil {
		t.Fatal("UNKNOWN permitted unrelated event")
	}
	if err := Append(path, "ri.lexical-observed", RILexicalObservation{IntentID: id, Build: &ri.LexicalBuild{ManifestID: manifestID, Shards: []ri.LexicalShard{}}}); err == nil {
		t.Fatal("incomplete build closed UNKNOWN")
	}
	after, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(failedJournal, after) {
		t.Fatal("rejected retry/observation changed journal", err)
	}
}
