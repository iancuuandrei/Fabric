package control

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/ri"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/verification"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func riGitCreation(t *testing.T) Creation {
	t.Helper()
	c := creation(t)
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial"}} {
		if output, err := exec.Command("git", append([]string{"-C", c.Repository.Root}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	}
	identity, err := repository.Discover(context.Background(), c.Repository.Root, c.Repository.Name)
	if err != nil {
		t.Fatal(err)
	}
	c.Repository = identity
	return c
}

func TestRIImportActualRustController(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("requires built Rust RI executable")
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	c := riGitCreation(t)
	committed := []byte("package fixture\n")
	workingSource := filepath.Join(c.Repository.Root, "source.go")
	if err := os.WriteFile(workingSource, committed, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "source.go"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "source"}} {
		if output, err := exec.Command("git", append([]string{"-C", c.Repository.Root}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	}
	c.Repository, err = repository.Discover(context.Background(), c.Repository.Root, c.Repository.Name)
	if err != nil {
		t.Fatal(err)
	}
	inputSource := filepath.Join(t.TempDir(), "source.go")
	if err := os.WriteFile(workingSource, []byte("dirty checkout"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(c.Config.Planner, c.Objective)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&runtime.Fake{}).Execute(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "plan.recorded", result); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "plan.approved", Approval{s.PlanID, "operator"}); err != nil {
		t.Fatal(err)
	}
	lexical, err := ri.ObserveLexical(context.Background(), c.Repository)
	if err != nil {
		t.Fatal(err)
	}
	manifestID, err := lexical.Manifest.ID()
	if err != nil {
		t.Fatal(err)
	}
	lexicalPlan := ri.LexicalPlan{Version: 1, Repository: c.Repository, ManifestID: manifestID, Files: len(lexical.Manifest.Files), Executable: executable, ExecutableSHA256: fmt.Sprintf("%x", sha256.Sum256(binary)), StageRoot: filepath.Join(t.TempDir(), "lexical"), BatchBytes: 1048576, BatchFiles: 100}
	lexicalIntent, err := lexicalPlan.Intent(s.RunID, s.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	lexicalID, err := lexicalIntent.ID()
	if err != nil {
		t.Fatal(err)
	}
	// Retain the pre-effect journal to model a lost completion observation.
	prefix, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lexicalState, err := ExecuteRILexical(context.Background(), path, lexicalPlan, lexical.Manifest, effects.Authorization{IntentID: lexicalID, Actor: "operator"})
	if err != nil || lexicalState.RILexical == nil || lexicalState.RILexical.Outcome != "CONFIRMED" {
		t.Fatal("journaled lexical indexing failed", err)
	}
	if _, err := ExecuteRILexical(context.Background(), path, lexicalPlan, lexical.Manifest, effects.Authorization{IntentID: lexicalID, Actor: "operator"}); err == nil {
		t.Fatal("lexical indexing was repeated")
	}
	recoveryPath := filepath.Join(t.TempDir(), "recovery.jsonl")
	if err := os.WriteFile(recoveryPath, prefix, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Append(recoveryPath, "ri.lexical-intent", RILexicalIntent{lexicalPlan, lexicalIntent, effects.Authorization{IntentID: lexicalID, Actor: "operator"}}); err != nil {
		t.Fatal(err)
	}
	recovered, err := ReconcileRILexical(context.Background(), recoveryPath)
	if err != nil || recovered.RILexical == nil || recovered.RILexical.Outcome != "CONFIRMED" {
		t.Fatal("lost lexical observation not recovered", err)
	}
	if _, err := ReconcileRILexical(context.Background(), recoveryPath); err == nil {
		t.Fatal("confirmed lexical effect reconciled twice")
	}
	source, err := ri.FromRepository(c.Repository)
	if err != nil {
		t.Fatal(err)
	}
	// Minimal SCIP document bound to an observed commit and an exact source copy.
	field := func(tag byte, data []byte) []byte { return append([]byte{tag, byte(len(data))}, data...) }
	tool := append(field(10, []byte("fixture")), field(18, []byte("1"))...)
	meta := append(field(18, tool), field(26, []byte("file:///fixture"))...)
	index := field(10, append(meta, 32, 1))
	document := append(field(10, []byte("source.go")), 48, 1)
	index = append(index, field(18, document)...)
	indexPath := filepath.Join(c.Repository.Root, "index.scip")
	if err := os.WriteFile(indexPath, index, 0600); err != nil {
		t.Fatal(err)
	}
	plan := ri.ImportPlan{Version: 1, Executable: executable, ExecutableSHA256: fmt.Sprintf("%x", sha256.Sum256(binary)), Repository: c.Repository, OutputPath: filepath.Join(c.Repository.Root, "snapshot.staging"), Request: ri.ImportRequest{IndexPath: indexPath, Source: source, Manifest: ri.Manifest{Format: 1, Source: source, Producers: []ri.Producer{{ID: "p", Name: "fixture", Version: "1", ArtifactSHA256: strings.Repeat("c", 64), Inputs: []ri.Input{{Name: "scip:index", SHA256: fmt.Sprintf("%x", sha256.Sum256(index))}}}}}, Sources: map[string]string{}, Producer: "p", Policy: "strict", ProjectRoot: "file:///fixture"}}
	plan.Request.Sources["source.go"] = inputSource
	plan.MaterializeSources = true
	plan.Request.Manifest.Producers[0].Inputs = append(plan.Request.Manifest.Producers[0].Inputs, ri.Input{Name: "source:source.go", SHA256: fmt.Sprintf("%x", sha256.Sum256(committed))})
	intent, err := plan.Intent(s.RunID, s.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	auth := effects.Authorization{IntentID: id, Actor: "operator"}
	s, err = ExecuteRIImport(context.Background(), path, plan, auth)
	if err != nil || s.RIImport == nil || s.RIImport.Outcome != "CONFIRMED" {
		t.Fatal("import not confirmed", err)
	}
	materialized, err := os.ReadFile(inputSource)
	if err != nil || string(materialized) != string(committed) {
		t.Fatal("controller did not materialize exact source", err)
	}
	replayed, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.RIImport.Observation.Artifact.SnapshotID != s.RIImport.Observation.Artifact.SnapshotID {
		t.Fatal("replay lost artifact identity")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteRIImport(context.Background(), path, plan, auth); err == nil {
		t.Fatal("confirmed effect repeated")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rejected duplicate changed journal")
	}
	plan.OutputPath = filepath.Join(c.Repository.Root, "interrupted.staging")
	plan.MaterializeSources = false
	intent, err = plan.Intent(s.RunID, s.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	id, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	auth = effects.Authorization{IntentID: id, Actor: "operator"}
	if err := Append(path, "ri.import-intent", RIImportIntent{plan, intent, auth}); err != nil {
		t.Fatal(err)
	}
	s, err = ReconcileRIImport(context.Background(), path)
	if err == nil || s.RIImport.Outcome != "UNKNOWN" {
		t.Fatal("missing staging was confirmed")
	}
	if _, err := os.Stat(plan.OutputPath); !os.IsNotExist(err) {
		t.Fatal("reconciliation created staging", err)
	}
	client := ri.Client{Executable: plan.Executable, ExecutableHash: plan.ExecutableSHA256}
	receipt, err := client.Import(context.Background(), plan.Request, plan.Repository, plan.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	// The process completed, but no observation was appended before interruption.
	staged, err := os.ReadFile(plan.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.OutputPath, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = ReconcileRIImport(context.Background(), path)
	if err == nil || s.RIImport.Outcome != "UNKNOWN" {
		t.Fatal("partial staging confirmed")
	}
	unchanged, err := os.ReadFile(plan.OutputPath)
	if err != nil || string(unchanged) != "partial" {
		t.Fatal("reconciliation rewrote partial bytes", err)
	}
	if err := os.WriteFile(plan.OutputPath, staged, 0600); err != nil {
		t.Fatal(err)
	}
	s, err = ReconcileRIImport(context.Background(), path)
	if err != nil || s.RIImport.Outcome != "CONFIRMED" || s.RIImport.Observation.Artifact.SnapshotID != receipt.SnapshotID {
		t.Fatal("completed staging not reconciled", err)
	}
	if _, err := ReconcileRIImport(context.Background(), path); err == nil {
		t.Fatal("closed effect reconciled twice")
	}
	storeDir := t.TempDir()
	if _, err := SelectRuntimeRI(context.Background(), path); err == nil {
		t.Fatal("unpublished import admitted to runtime")
	}
	publication, err := PrepareRIPublish(path, storeDir)
	if err != nil {
		t.Fatal(err)
	}
	publicationID, err := publication.ID()
	if err != nil {
		t.Fatal(err)
	}
	publicationAuth := effects.Authorization{IntentID: publicationID, Actor: "operator"}
	s, err = ExecuteRIPublish(context.Background(), path, storeDir, publicationAuth)
	if err != nil || s.RIPublish == nil || s.RIPublish.Outcome != "CONFIRMED" {
		t.Fatal("publication failed", err)
	}
	published, err := (ri.Store{Directory: storeDir}).Read(receipt.SnapshotID)
	if err != nil || ri.SnapshotID(published) != receipt.SnapshotID {
		t.Fatal("published bytes mismatch", err)
	}
	binding, err := SelectRuntimeRI(context.Background(), path)
	if err != nil || binding.Snapshot != *s.RIPublish.Observation.Artifact || binding.ExecutableSHA256 != plan.ExecutableSHA256 {
		t.Fatal("runtime binding not selected from confirmed publication", err)
	}
	s, err = StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	withWriter := s
	profile := s.Creation.Config.Planner
	profile.Role = "writer"
	withWriter.Creation.Config.Writer = &profile
	withRI, err := writerInvocation(withWriter)
	if err != nil {
		t.Fatal(err)
	}
	var contextInput struct {
		RI *roleRIContext `json:"ri"`
	}
	if err := json.Unmarshal([]byte(withRI.Input), &contextInput); err != nil {
		t.Fatal(err)
	}
	if contextInput.RI == nil || contextInput.RI.Scope != "base_commit" || contextInput.RI.Binding != binding {
		t.Fatal("writer RI context not bound to publication")
	}
	withoutRI := withWriter
	withoutRI.RIPublish = nil
	plain, err := writerInvocation(withoutRI)
	if err != nil || plain.ID == withRI.ID {
		t.Fatal("RI snapshot not bound to writer invocation", err)
	}
	if optional, err := roleRI(withoutRI); err != nil || optional != nil {
		t.Fatal("missing publication fabricated RI context", err)
	}
	// This snapshot tests input identity only; it does not admit a review event.
	withReviewer := s
	reviewer := profile
	reviewer.Role = "reviewer"
	withReviewer.Creation.Config.Reviewer = &reviewer
	withReviewer.State = "REVIEWING"
	candidateID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	withReviewer.Verification = &VerificationState{Plan: verification.Plan{CandidateID: candidateID}}
	reviewWithRI, err := reviewInvocation(withReviewer)
	if err != nil {
		t.Fatal(err)
	}
	contextInput.RI = nil
	if err := json.Unmarshal([]byte(reviewWithRI.Input), &contextInput); err != nil {
		t.Fatal(err)
	}
	if contextInput.RI == nil || contextInput.RI.Scope != "base_commit" || contextInput.RI.Binding != binding {
		t.Fatal("review RI context not bound to publication")
	}
	withReviewer.RIPublish = nil
	reviewWithoutRI, err := reviewInvocation(withReviewer)
	if err != nil || reviewWithoutRI.ID == reviewWithRI.ID {
		t.Fatal("RI snapshot not bound to review invocation", err)
	}
	if _, err := ExecuteRIPublish(context.Background(), path, storeDir, publicationAuth); err == nil {
		t.Fatal("publication replayed")
	}
	secondStore := t.TempDir()
	publication, err = PrepareRIPublish(path, secondStore)
	if err != nil {
		t.Fatal(err)
	}
	publicationID, err = publication.ID()
	if err != nil {
		t.Fatal(err)
	}
	publicationAuth = effects.Authorization{IntentID: publicationID, Actor: "operator"}
	if err := Append(path, "ri.publish-intent", RIPublishIntent{secondStore, publication, publicationAuth}); err != nil {
		t.Fatal(err)
	}
	s, err = ReconcileRIPublish(context.Background(), path)
	if err == nil || s.RIPublish.Outcome != "UNKNOWN" {
		t.Fatal("missing publication confirmed")
	}
	if _, err := SelectRuntimeRI(context.Background(), path); err == nil {
		t.Fatal("unresolved publication admitted to runtime")
	}
	if _, err := (ri.Store{Directory: secondStore}).Publish(receipt.SnapshotID, published); err != nil {
		t.Fatal(err)
	}
	s, err = ReconcileRIPublish(context.Background(), path)
	if err != nil || s.RIPublish.Outcome != "CONFIRMED" {
		t.Fatal("completed publication not reconciled", err)
	}
	thirdStore := t.TempDir()
	publication, err = PrepareRIPublish(path, thirdStore)
	if err != nil {
		t.Fatal(err)
	}
	publicationID, err = publication.ID()
	if err != nil {
		t.Fatal(err)
	}
	publicationAuth = effects.Authorization{IntentID: publicationID, Actor: "operator"}
	if err := Append(path, "ri.publish-intent", RIPublishIntent{thirdStore, publication, publicationAuth}); err != nil {
		t.Fatal(err)
	}
	pendingPath := filepath.Join(thirdStore, receipt.SnapshotID+".pending")
	if err := os.WriteFile(pendingPath, published, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileRIPublish(context.Background(), path); err == nil {
		t.Fatal("pending publication treated as final")
	}
	recovery, err := PrepareRIPublishRecovery(path)
	if err != nil {
		t.Fatal(err)
	}
	recoveryID, err := recovery.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverRIPublish(context.Background(), path, publicationAuth); err == nil {
		t.Fatal("publication approval authorized recovery")
	}
	if err := os.WriteFile(pendingPath, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = RecoverRIPublish(context.Background(), path, effects.Authorization{IntentID: recoveryID, Actor: "operator"})
	if err == nil || s.RIPublish.Outcome != "UNKNOWN" {
		t.Fatal("partial recovery was confirmed")
	}
	next, err := PrepareRIPublishRecovery(path)
	if err != nil {
		t.Fatal(err)
	}
	nextID, err := next.ID()
	if err != nil || nextID == recoveryID {
		t.Fatal("recovery attempt identity reused", err)
	}
	if _, err := RecoverRIPublish(context.Background(), path, effects.Authorization{IntentID: recoveryID, Actor: "operator"}); err == nil {
		t.Fatal("stale recovery approval admitted")
	}
	if err := os.WriteFile(pendingPath, published, 0600); err != nil {
		t.Fatal(err)
	}
	recoveryID = nextID
	s, err = RecoverRIPublish(context.Background(), path, effects.Authorization{IntentID: recoveryID, Actor: "operator"})
	if err != nil || s.RIPublish.Outcome != "CONFIRMED" || s.RIPublish.Recovery == nil {
		t.Fatal("pending publication recovery failed", err)
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatal("pending name survived recovery", err)
	}
	if _, err := PrepareRIPublishRecovery(path); err == nil {
		t.Fatal("repeated recovery admitted")
	}
	plan.OutputPath = filepath.Join(c.Repository.Root, "new-import.staging")
	intent, err = plan.Intent(s.RunID, s.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	id, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	s, err = ExecuteRIImport(context.Background(), path, plan, effects.Authorization{IntentID: id, Actor: "operator"})
	if err != nil || s.RIImport.Outcome != "CONFIRMED" {
		t.Fatal("fresh import failed", err)
	}
	if _, err := SelectRuntimeRI(context.Background(), path); err == nil {
		t.Fatal("old publication reused with a different import intent")
	}
}

func TestRIImportFailureRemainsDurableUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	c := riGitCreation(t)
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(c.Config.Planner, c.Objective)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&runtime.Fake{}).Execute(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "plan.recorded", result); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "plan.approved", Approval{s.PlanID, "operator"}); err != nil {
		t.Fatal(err)
	}
	source, err := ri.FromRepository(c.Repository)
	if err != nil {
		t.Fatal(err)
	}
	plan := ri.ImportPlan{Version: 1, Executable: filepath.Join(c.Repository.Root, "missing.exe"), ExecutableSHA256: strings.Repeat("c", 64), Repository: c.Repository, OutputPath: filepath.Join(c.Repository.Root, "snapshot.staging"), Request: ri.ImportRequest{IndexPath: filepath.Join(c.Repository.Root, "index.scip"), Source: source, Manifest: ri.Manifest{Format: 1, Source: source, Producers: []ri.Producer{{ID: "p", Inputs: []ri.Input{}}}}, Sources: map[string]string{}, Producer: "p", Policy: "strict"}}
	intent, err := plan.Intent(s.RunID, s.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	auth := effects.Authorization{IntentID: id, Actor: "operator"}
	s, err = ExecuteRIImport(context.Background(), path, plan, auth)
	if err == nil || s.RIImport == nil || s.RIImport.Outcome != "UNKNOWN" {
		t.Fatal("failed import lost uncertainty", s.RIImport, err)
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-2].Kind != "ri.import-intent" || events[len(events)-1].Kind != "ri.import-observed" {
		t.Fatal("missing durable event ordering")
	}
	if _, err := ExecuteRIImport(context.Background(), path, plan, auth); err == nil {
		t.Fatal("unknown import automatically retried")
	}
	bad := RIImportObservation{IntentID: id, Artifact: &ri.ImportReceipt{SnapshotID: strings.Repeat("d", 64), Bytes: 1, Source: source, OutputPath: filepath.Join(c.Repository.Root, "foreign")}}
	if err := Append(path, "ri.import-observed", bad); err == nil {
		t.Fatal("foreign artifact admitted")
	}
	s, err = Inspect(path)
	if err != nil || s.RIImport.Outcome != "UNKNOWN" {
		t.Fatal("rejected observation changed state", err)
	}
}
