package control

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexhost"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func TestReviewImpactContextPolicyAndHistoryAreBounded(t *testing.T) {
	valid := ExecutionPolicy{
		Mode: "autonomous-v1", MaxRepairs: 2,
		PlannerContext:                   plannerContextGoContractV1,
		PlannerContextRIExecutable:       `C:\tools\ri.exe`,
		PlannerContextRIExecutableSHA256: strings.Repeat("a", 64),
		ReviewImpactContextVersion:       reviewImpactContextVersion1,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid review-impact policy rejected: %v", err)
	}
	receiverAware := valid
	receiverAware.PlannerContext = plannerContextGoContractV2
	if err := receiverAware.Validate(); err != nil {
		t.Fatalf("receiver-aware review-impact policy rejected: %v", err)
	}
	contractV3 := valid
	contractV3.PlannerContext = plannerContextGoContractV3
	if err := contractV3.Validate(); err != nil {
		t.Fatalf("contract v3 review-impact policy rejected: %v", err)
	}
	cache := valid
	cache.CandidateFactsCacheVersion = 1
	if err := cache.Validate(); err != nil {
		t.Fatalf("valid candidate facts cache policy rejected: %v", err)
	}
	for name, policy := range map[string]ExecutionPolicy{
		"candidate cache without review context": {Mode: "autonomous-v1", MaxRepairs: 2, PlannerContext: plannerContextGoContractV1, PlannerContextRIExecutable: `C:\tools\ri.exe`, PlannerContextRIExecutableSHA256: strings.Repeat("a", 64), CandidateFactsCacheVersion: 1},
		"without contract context":               {Mode: "autonomous-v1", MaxRepairs: 2, ReviewImpactContextVersion: 1},
		"without pinned parser": {
			Mode: "autonomous-v1", MaxRepairs: 2, PlannerContext: plannerContextGoContractV1,
			PlannerContextRIExecutableSHA256: strings.Repeat("a", 64), ReviewImpactContextVersion: 1,
		},
		"unknown version": {
			Mode: "autonomous-v1", MaxRepairs: 2, PlannerContext: plannerContextGoContractV1,
			PlannerContextRIExecutable: `C:\tools\ri.exe`, PlannerContextRIExecutableSHA256: strings.Repeat("a", 64), ReviewImpactContextVersion: 2,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := policy.Validate(); err == nil {
				t.Fatal("invalid review-impact policy accepted")
			}
		})
	}

	for repairs, want := range map[int]int{0: 1, 1: 2, 8: reviewImpactMaxHistory} {
		snapshot := Snapshot{Creation: Creation{Execution: &ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: repairs}}}
		if got := reviewImpactHistoryLimit(&snapshot); got != want {
			t.Fatalf("repair budget %d produced history bound %d, want %d", repairs, got, want)
		}
	}
	if reviewImpactHistoryLimit(nil) != 0 || reviewImpactHistoryLimit(&Snapshot{}) != 0 {
		t.Fatal("history is unbounded when the immutable execution policy is absent")
	}
}

func TestReviewImpactContextJSONDoesNotExposeCorpusInPrompt(t *testing.T) {
	record := &ReviewImpactContextRecord{
		Version: 1, CandidateID: strings.Repeat("b", 64), CandidateFilesHash: strings.Repeat("c", 64),
		CandidateGraphDigest: strings.Repeat("d", 64), CandidateModuleInventoryDigest: strings.Repeat("e", 64),
		UnavailableReason: "candidate_review_record_budget", ChangedPathCount: 2,
	}
	prompt, err := json.Marshal(reviewImpactPromptFor(record))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(prompt), "corpus") || strings.Contains(string(prompt), "source_content") {
		t.Fatalf("review prompt included durable corpus data: %s", prompt)
	}
	if !strings.Contains(string(prompt), `"coverage":"PARTIAL"`) || !strings.Contains(string(prompt), `"unavailable_reason":"candidate_review_record_budget"`) {
		t.Fatalf("review prompt omitted partial/unavailable disclosure: %s", prompt)
	}
}

func TestReviewImpactContextAdmitsAndReplaysExactCandidateProjection(t *testing.T) {
	ctx := context.Background()
	creation := autonomousCreation(t, 1)
	root := creation.Repository.Root
	autonomousGitInit(t, root)
	for name, content := range map[string]string{
		"go.mod": "module example.test/review-impact\n\ngo 1.23\n",
		"app.go": "package sample\n// PRIVATE_REVIEW_IMPACT_SOURCE_SENTINEL\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "Go base"}} {
		if out, err := autonomousGitCmd(t, root, args); err != nil {
			t.Fatal(err, string(out))
		}
	}
	identity, err := repository.Discover(ctx, root, creation.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	creation.Repository = identity
	producer := strings.Repeat("a", 64)
	creation.Execution.PlannerContext = plannerContextGoContractV1
	creation.Execution.PlannerContextRIExecutable = filepath.Join(root, "fixture-ri.exe")
	creation.Execution.PlannerContextRIExecutableSHA256 = producer
	creation.Execution.ReviewImpactContextVersion = 1
	source, err := ri.FromRepository(identity)
	if err != nil {
		t.Fatal(err)
	}
	baseInventory, err := ri.CollectGoModuleInventory(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	baseContent := "package sample\n// PRIVATE_REVIEW_IMPACT_SOURCE_SENTINEL\n"
	baseFile := focusPromptGraphInput("app.go", baseContent, ri.GoPackageBinding{}, nil, nil, producer)
	baseFile.Package, err = ri.DeclaredGoPackageBinding(baseInventory, "app.go", "sample")
	if err != nil {
		t.Fatal(err)
	}
	baseGraph, err := ri.BuildGoEngineeringGraph(ri.GoGraphSnapshotInput{
		SourceID: source.RepositoryID, ProducerSHA256: producer,
		Files: []ri.GoGraphFileInput{baseFile}, Generators: []ri.GoGeneratorBinding{}, ModuleInventory: &baseInventory,
	})
	if err != nil {
		t.Fatal(err)
	}

	runID := strings.Repeat("1", 64)
	request, err := worktree.Prepare(runID, identity)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := worktree.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	candidateContent := "package sample \n// PRIVATE_REVIEW_IMPACT_SOURCE_SENTINEL\n"
	if err := os.WriteFile(filepath.Join(request.Path, "app.go"), []byte(candidateContent), 0600); err != nil {
		t.Fatal(err)
	}
	candidate, _, err := worktree.Capture(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	candidateID, err := candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	candidateInventory, err := ri.CollectCandidateGoModuleInventory(ctx, identity, binding, candidate, baseInventory)
	if err != nil {
		t.Fatal(err)
	}
	candidateFile := focusPromptGraphInput("app.go", candidateContent, ri.GoPackageBinding{}, nil, nil, producer)
	candidateFile.Package, err = ri.DeclaredGoPackageBinding(candidateInventory, "app.go", "sample")
	if err != nil {
		t.Fatal(err)
	}
	candidateGraph, err := ri.ApplyGoEngineeringOverlay(baseGraph, ri.GoGraphOverlayInput{
		BaseDigest: baseGraph.Digest, CandidateID: candidateID, ProducerSHA256: producer,
		Replacements: []ri.GoGraphFileInput{candidateFile}, Deleted: []string{}, Generators: []ri.GoGeneratorBinding{}, ModuleInventory: &candidateInventory,
	})
	if err != nil {
		t.Fatal(err)
	}
	corpus := ri.GoCandidateCorpus{
		Version: 1, Source: source, CandidateID: candidateID, CandidateFilesHash: candidate.FilesHash,
		ProducerSHA256: producer, BaseGraphDigest: baseGraph.Digest, BaseModuleInventoryDigest: baseInventory.Digest,
		ModuleInventory: candidateInventory, Graph: candidateGraph,
		ChangedPaths: []string{"app.go"}, AdmittedPaths: []string{}, DeletedPaths: []string{},
		Omissions: []taskcontext.Omission{}, Coverage: "PARTIAL",
	}
	snapshot := Snapshot{
		RunID: runID, State: "REVIEWING", Creation: Creation{Repository: identity, Execution: creation.Execution},
		Workspace: &binding, Candidate: &candidate,
		PlannerGoContext: &PlannerGoContextRecord{
			Version: 4, Source: source, RIExecutableSHA256: producer, RecordID: strings.Repeat("2", 64), Graph: &baseGraph,
			ContractContext: &ri.GoContractContext{SourceID: source.RepositoryID, GraphDigest: baseGraph.Digest, ProducerSHA256: producer, Coverage: "PARTIAL", Excerpts: []taskcontext.SelectedFile{}},
		},
	}
	snapshot.Review, snapshot.ReviewHost = reviewImpactPriorReview(t, strings.Repeat("9", 64), strings.Repeat("8", 64))
	record, err := makeReviewImpactContextRecord(snapshot, source, baseGraph, baseInventory, corpus)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReviewImpactContextRecord(snapshot, record); err != nil {
		t.Fatalf("new candidate after settled changes-requested review did not validate: %v", err)
	}
	receiverAwareSnapshot := snapshot
	receiverAwareExecution := *snapshot.Creation.Execution
	receiverAwareExecution.PlannerContext = plannerContextGoContractV2
	receiverAwareSnapshot.Creation.Execution = &receiverAwareExecution
	receiverAwarePlanner := *snapshot.PlannerGoContext
	receiverAwarePlanner.Version = 5
	receiverAwarePlanner.CorpusSelectionVersion = ri.GoCorpusSelectionReceiverAwareV2
	receiverAwarePlanner.RecordID = strings.Repeat("3", 64)
	receiverAwareSnapshot.PlannerGoContext = &receiverAwarePlanner
	receiverAwareRecord, err := makeReviewImpactContextRecord(receiverAwareSnapshot, source, baseGraph, baseInventory, corpus)
	if err != nil || validateReviewImpactContextRecord(receiverAwareSnapshot, receiverAwareRecord) != nil {
		t.Fatalf("receiver-aware contract evidence did not admit review-impact context: make=%v validate=%v", err, validateReviewImpactContextRecord(receiverAwareSnapshot, receiverAwareRecord))
	}
	contractV3Snapshot := receiverAwareSnapshot
	contractV3Execution := *receiverAwareSnapshot.Creation.Execution
	contractV3Execution.PlannerContext = plannerContextGoContractV3
	contractV3Snapshot.Creation.Execution = &contractV3Execution
	contractV3Planner := *receiverAwareSnapshot.PlannerGoContext
	contractV3Planner.Version = 6
	contractV3Planner.RecordID = strings.Repeat("4", 64)
	contractV3Snapshot.PlannerGoContext = &contractV3Planner
	contractV3Record, err := makeReviewImpactContextRecord(contractV3Snapshot, source, baseGraph, baseInventory, corpus)
	if err != nil || validateReviewImpactContextRecord(contractV3Snapshot, contractV3Record) != nil {
		t.Fatalf("contract v3 evidence did not admit review-impact context: make=%v validate=%v", err, validateReviewImpactContextRecord(contractV3Snapshot, contractV3Record))
	}
	oversizedCorpus := corpus
	oversizedGraph := corpus.Graph
	oversizedGraph.Nodes = append([]ri.GoGraphNode{}, corpus.Graph.Nodes...)
	for index := 0; index < 9000; index++ {
		oversizedGraph.Nodes = append(oversizedGraph.Nodes, ri.GoGraphNode{ID: fmt.Sprintf("oversized-%05d", index), Kind: "call", Label: strings.Repeat("bounded-node-", 12), Path: "app.go", Resolution: "UNRESOLVED"})
	}
	oversizedCorpus.Graph = oversizedGraph
	fallback, err := makeReviewImpactContextRecord(receiverAwareSnapshot, source, baseGraph, baseInventory, oversizedCorpus)
	if err != nil || fallback.UnavailableReason != "candidate_review_record_budget" || fallback.Corpus != nil || fallback.Projection != nil {
		t.Fatalf("oversized candidate graph did not produce bounded unavailable fallback: reason=%q err=%v", fallback.UnavailableReason, err)
	}
	if err := validateReviewImpactContextRecord(receiverAwareSnapshot, fallback); err != nil {
		t.Fatalf("bounded unavailable fallback did not validate: %v", err)
	}
	riUnavailable, err := makeReviewImpactRIUnavailableRecord(receiverAwareSnapshot, source, baseGraph, baseInventory)
	if err != nil || riUnavailable.UnavailableReason != reviewImpactRIUnavailable || riUnavailable.CandidateGraphDigest != "" || riUnavailable.CandidateModuleInventoryDigest != "" {
		t.Fatalf("RI unavailability did not produce truthful source-free fallback: reason=%q err=%v", riUnavailable.UnavailableReason, err)
	}
	if err := validateReviewImpactContextRecord(receiverAwareSnapshot, riUnavailable); err != nil {
		t.Fatalf("candidate-bound RI-unavailable fallback rejected: %v", err)
	}
	var fallbackRoundTrip ReviewImpactContextRecord
	fallbackJSON, err := canonical.Bytes(riUnavailable)
	if err != nil || canonical.Decode(fallbackJSON, &fallbackRoundTrip) != nil {
		t.Fatalf("RI-unavailable fallback did not survive durable encoding: %v", err)
	}
	fallbackReplay := receiverAwareSnapshot
	fallbackReplay.ReviewImpactContexts = nil
	if err := replayReviewImpactContext(&fallbackReplay, fallbackRoundTrip); err != nil || len(fallbackReplay.ReviewImpactContexts) != 1 {
		t.Fatalf("RI-unavailable fallback did not replay: count=%d err=%v", len(fallbackReplay.ReviewImpactContexts), err)
	}
	forgedFallback := riUnavailable
	forgedFallback.CandidateGraphDigest = strings.Repeat("5", 64)
	forgedFallback.RecordID, err = reviewImpactContextRecordID(forgedFallback)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReviewImpactContextRecord(receiverAwareSnapshot, forgedFallback); err == nil {
		t.Fatal("RI-unavailable fallback accepted an unobserved candidate graph digest")
	}
	forgedFallback = riUnavailable
	forgedFallback.UnavailableReason = "parser error: " + "private details"
	forgedFallback.RecordID, err = reviewImpactContextRecordID(forgedFallback)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReviewImpactContextRecord(receiverAwareSnapshot, forgedFallback); err == nil {
		t.Fatal("RI-unavailable fallback accepted arbitrary error text")
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "PRIVATE_REVIEW_IMPACT_SOURCE_SENTINEL") {
		t.Fatal("durable review-impact record retained source text")
	}
	if err := replayReviewImpactContext(&snapshot, record); err != nil || len(snapshot.ReviewImpactContexts) != 1 {
		t.Fatalf("new-candidate record after repair did not durably replay: count=%d err=%v", len(snapshot.ReviewImpactContexts), err)
	}
	if err := replayReviewImpactContext(&snapshot, record); err == nil {
		t.Fatal("duplicate candidate review context replayed")
	}
	forged := record
	projection := *record.Projection
	projection.ChangedPathCount++
	forged.Projection = &projection
	forged.RecordID, err = reviewImpactContextRecordID(forged)
	if err != nil {
		t.Fatal(err)
	}
	fresh := snapshot
	fresh.ReviewImpactContexts = nil
	if err := replayReviewImpactContext(&fresh, forged); err == nil {
		t.Fatal("self-rehashed forged projection replayed")
	}
	priorCandidateID, _ := candidate.ID()
	sameCandidate := snapshot
	sameCandidate.Review, _ = reviewImpactPriorReview(t, priorCandidateID, strings.Repeat("8", 64))
	if err := validateReviewImpactCandidateBoundary(sameCandidate); err == nil {
		t.Fatal("review result bound to the current candidate was treated as stale")
	}

	pendingHost := snapshot
	pendingHost.ReviewHost = &ReviewHostState{}
	if err := validateReviewImpactCandidateBoundary(pendingHost); err == nil {
		t.Fatal("pending prior review host was treated as settled")
	}

	mismatchedHost := snapshot
	mismatchedHost.ReviewHost = &ReviewHostState{
		Ready: true, Receipt: &codexhost.Receipt{},
		Intent:         ReviewHostIntent{Invocation: runtime.Invocation{ID: strings.Repeat("a", 64)}},
		RuntimeReceipt: &ReviewRuntimeReceipt{InvocationID: strings.Repeat("a", 64), ThreadID: "thread", TurnID: "turn", JournalHead: strings.Repeat("b", 64), ResultHash: strings.Repeat("c", 64)},
	}
	if err := validateReviewImpactCandidateBoundary(mismatchedHost); err == nil {
		t.Fatal("mismatched prior host and review were treated as settled")
	}

	mismatchedResult := snapshot
	resultReview := *snapshot.Review
	var wrongVerdict ReviewVerdict
	if err := canonical.Decode([]byte(resultReview.Result.Output), &wrongVerdict); err != nil {
		t.Fatal(err)
	}
	wrongVerdict.CandidateID = strings.Repeat("7", 64)
	wrongOutput, err := canonical.Bytes(wrongVerdict)
	if err != nil {
		t.Fatal(err)
	}
	resultReview.Result.Output = string(wrongOutput)
	mismatchedResult.Review = &resultReview
	if err := validateReviewImpactCandidateBoundary(mismatchedResult); err == nil {
		t.Fatal("prior review result not bound to its invocation was treated as settled")
	}

	malformed := snapshot
	badReview := *snapshot.Review
	badReview.Result.Output = "not-json"
	malformed.Review = &badReview
	if err := validateReviewImpactCandidateBoundary(malformed); err == nil {
		t.Fatal("malformed prior review was treated as settled")
	}

}

func reviewImpactPriorReview(t *testing.T, candidateID, verificationPlanID string) (*ReviewRecord, *ReviewHostState) {
	t.Helper()
	profile := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "review-fixture", Effort: "high", Role: "reviewer"}
	input, err := canonical.Bytes(struct {
		Instruction        string `json:"instruction"`
		RunID              string `json:"run_id"`
		PlanID             string `json:"plan_id"`
		CandidateID        string `json:"candidate_id"`
		VerificationPlanID string `json:"verification_plan_id"`
		Objective          string `json:"objective"`
		Plan               string `json:"plan"`
	}{"Review this prior candidate.", strings.Repeat("1", 64), strings.Repeat("2", 64), candidateID, verificationPlanID, "fixture objective", "fixture plan"})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(profile, string(input))
	if err != nil {
		t.Fatal(err)
	}
	output, err := canonical.Bytes(ReviewVerdict{
		CandidateID: candidateID, VerificationPlanID: verificationPlanID,
		Decision: "changes_requested", Findings: []ReviewFinding{{Path: "app.go", Message: "repair required"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	model := profile.Model
	result := runtime.Result{
		Version: 1, InvocationID: invocation.ID, Requested: profile, ObservedModel: &model, Output: string(output),
	}
	launch, hostReceipt := codexReceiptReplayFixture(t)
	resultHash, err := canonical.Hash("harness.review-result.v1", result)
	if err != nil {
		t.Fatal(err)
	}
	record := &ReviewRecord{Invocation: invocation, Result: result}
	host := &ReviewHostState{
		Intent: ReviewHostIntent{Invocation: invocation, Launch: launch},
		Ready:  true, Receipt: &hostReceipt,
		RuntimeReceipt: &ReviewRuntimeReceipt{
			InvocationID: invocation.ID, ThreadID: "fixture-thread", TurnID: "fixture-turn",
			JournalHead: strings.Repeat("b", 64), ResultHash: resultHash,
		},
	}
	return record, host
}

func TestReviewImpactDisabledKeepsLegacyReviewPayloadShape(t *testing.T) {
	c := creation(t)
	c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-writer", Effort: "high", Role: "writer"}
	c.Config.Reviewer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-reviewer", Effort: "high", Role: "reviewer"}
	c.Config.Verification = []config.Check{{Name: "git-version", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}}
	path, _ := approvedRepositoryCreation(t, c)
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := reviewInvocation(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal([]byte(invocation.Input), &input); err != nil {
		t.Fatal(err)
	}
	if _, exists := input["review_impact_context"]; exists {
		t.Fatal("disabled feature changed the legacy reviewer payload shape")
	}
	if strings.Contains(string(input["instruction"]), "review-impact context") {
		t.Fatal("disabled feature changed the legacy reviewer instruction")
	}
}
