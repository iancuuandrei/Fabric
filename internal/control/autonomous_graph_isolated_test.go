package control

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/controllerstate"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
)

func isolatedThreeWriterGraphFixture() engineeringplan.Graph {
	return engineeringplan.Graph{Version: engineeringplan.Version, Mode: engineeringplan.ModeGraph, Summary: "three isolated initial implementations", Tasks: []engineeringplan.Task{
		{ID: "impl-alpha", Kind: engineeringplan.Implementation, Title: "Create alpha output", ScopePaths: []string{"."}, WritePaths: []string{"alpha.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "alpha output exists"}}, EstimatedSeconds: 30},
		{ID: "impl-beta", Kind: engineeringplan.Implementation, Title: "Create beta output", ScopePaths: []string{"."}, WritePaths: []string{"beta.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "beta output exists"}}, EstimatedSeconds: 30},
		{ID: "impl-gamma", Kind: engineeringplan.Implementation, Title: "Create gamma output", ScopePaths: []string{"."}, WritePaths: []string{"gamma.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "gamma output exists"}}, EstimatedSeconds: 30},
		{ID: "verify", Kind: engineeringplan.Verification, Title: "Run native checks", Dependencies: []string{"impl-alpha", "impl-beta", "impl-gamma"}, ScopePaths: []string{"."}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "check", Description: "configured checks pass"}}, EstimatedSeconds: 30},
		{ID: "review", Kind: engineeringplan.Review, Title: "Review candidate", Dependencies: []string{"verify"}, ScopePaths: []string{"."}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "review", Description: "approve the verified candidate"}}, EstimatedSeconds: 30},
	}}
}

func isolatedThreeWriterCreation(t *testing.T) Creation {
	t.Helper()
	c := parallelWriterCreation(t, 3)
	c.Execution.ParallelImplementationVersion = 0
	c.Execution.IsolatedImplementationVersion = 1
	c.Config.PlannerContract = plannerContractGraphV7
	c.Config.WriterContract = "anchored-edits-v1"
	profileID, err := canonical.Hash("harness.isolation-writer-profile.v1", *c.Config.Writer)
	if err != nil {
		t.Fatal(err)
	}
	model := engineeringplan.ProviderModelKey{Provider: c.Config.Writer.Provider, Model: c.Config.Writer.Model}
	route := engineeringplan.RuntimeResourceKey{ProfileID: profileID, Provider: model.Provider, Model: model.Model}
	c.Execution.IsolationEstimate = &IsolationEstimateTemplate{CPUMilli: 1, MemoryMiB: 1, VerificationSlots: 1, RuntimeSlots: 1}
	c.Execution.IsolationCapacity = &engineeringplan.ResourceCapacity{
		CPUMilli: 3, MemoryMiB: 3, VerificationSlots: 3, TotalRuntimeSlots: 3,
		ProviderSlots: []engineeringplan.ProviderSlotLimit{{Provider: model.Provider, Slots: 3}},
		ModelSlots:    []engineeringplan.ModelSlotLimit{{Model: model, Slots: 3}},
		RuntimeSlots:  []engineeringplan.RuntimeSlotLimit{{Runtime: route, Slots: 3}},
	}
	c.Config.ControllerStateRoot = filepath.Join(t.TempDir(), "controller-state")
	if err := c.Execution.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGraphWriterRecordOmitsZeroLegacyWriterOnlyForIsolatedEvidence(t *testing.T) {
	isolated, err := canonical.Bytes(GraphWriterRecord{TaskID: "impl-a", Isolated: &IsolatedGraphWriterProposal{Version: 1, TaskID: "impl-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(isolated), `"writer"`) || !strings.Contains(string(isolated), `"isolated"`) {
		t.Fatalf("isolated record serialized a synthetic legacy writer: %s", isolated)
	}
	legacy, err := canonical.Bytes(GraphWriterRecord{TaskID: "impl-a", Writer: WriterRecord{Invocation: runtime.Invocation{ID: strings.Repeat("a", 64)}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(legacy), `"writer"`) || strings.Contains(string(legacy), `"isolated"`) {
		t.Fatalf("legacy writer record shape changed: %s", legacy)
	}
	if got := string(legacy); got != `{"task_id":"impl-a","writer":{"invocation":{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","input":"","profile":{"effort":"","model":"","provider":"","role":"","runtime":""},"version":0},"prepared":{"intent":{"input_hash":"","kind":"","plan_id":"","repository_id":"","run_id":"","version":0},"proposal":{"after":{"file_count":0,"files_hash":"","head":"","index_hash":"","version":0,"worktree_id":""},"before":{"file_count":0,"files_hash":"","head":"","index_hash":"","version":0,"worktree_id":""},"before_files":null,"changes":null,"nonce":"","version":0}},"result":{"invocation_id":"","observed_effort":null,"observed_model":null,"observed_provider":null,"output":"","requested":{"effort":"","model":"","provider":"","role":"","runtime":""},"usage":{"cost_minor_units":null,"input_tokens":null,"output_tokens":null},"version":0}}}` {
		t.Fatalf("legacy graph writer record canonical bytes changed: %s", got)
	}
	if _, err := canonical.Bytes(GraphWriterRecord{TaskID: "impl-a", Writer: WriterRecord{Invocation: runtime.Invocation{ID: strings.Repeat("a", 64)}}, Isolated: &IsolatedGraphWriterProposal{Version: 1, TaskID: "impl-a"}}); err == nil {
		t.Fatal("mixed legacy and isolated writer payload was serialized")
	}
	forgedMixed := strings.TrimSuffix(string(isolated), "}") + `,"writer":{"invocation":{"version":0,"id":"","profile":{"runtime":"","provider":"","model":"","effort":"","role":""},"input":""},"result":{"version":0,"invocation_id":"","requested":{"runtime":"","provider":"","model":"","effort":"","role":""},"observed_model":null,"observed_provider":null,"observed_effort":null,"output":"","usage":{"input_tokens":null,"output_tokens":null,"cost_minor_units":null}},"prepared":{"proposal":{"version":0,"nonce":"","before":{"version":0,"worktree_id":"","head":"","index_hash":"","files_hash":"","file_count":0},"before_files":null,"changes":null,"after":{"version":0,"worktree_id":"","head":"","index_hash":"","files_hash":"","file_count":0}},"intent":{"version":0,"run_id":"","plan_id":"","repository_id":"","kind":"","input_hash":""}}}}`
	var decoded GraphWriterRecord
	if err := canonical.Decode([]byte(forgedMixed), &decoded); err == nil {
		t.Fatal("replay decoder accepted a forged mixed isolated/legacy payload")
	}
}

func TestIsolatedGraphThreeWritersIntegrateOnceAndPassNativeGates(t *testing.T) {
	c := isolatedThreeWriterCreation(t)
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, isolatedThreeWriterGraphFixture())
	machineAuthorizePlan(t, path, s)
	workspace, err := StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Config.Codex.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	ready, err := RunAutonomous(context.Background(), path)
	if err != nil {
		if latest, inspectErr := Inspect(path); inspectErr == nil {
			outcomes := map[string]string{}
			for id, isolation := range latest.GraphIsolations {
				outcomes[id] = isolation.Outcome
			}
			t.Logf("isolated run state=%s results=%d prepared-cohort=%v isolation-outcomes=%v", latest.State, len(latest.GraphWriterResults), latest.GraphIsolationPreparation != nil, outcomes)
		}
		if schedules, globErr := filepath.Glob(path + ".isolated-graph-writers-*.jsonl"); globErr == nil {
			for _, schedulePath := range schedules {
				if schedule, inspectErr := taskscheduler.Inspect(schedulePath); inspectErr == nil {
					statuses := map[string]string{}
					for id, state := range schedule.Tasks {
						statuses[id] = string(state.Status)
					}
					t.Logf("isolated schedule statuses: %v", statuses)
				}
			}
		}
		t.Fatal(err)
	}
	if ready.State != "READY" || ready.GraphWriterBatch == nil || ready.GraphWriterBatch.Version != 2 || len(ready.GraphWriterBatch.Members) != 3 {
		t.Fatalf("isolated graph did not reach READY with a three-member v2 aggregate: state=%s batch=%+v", ready.State, ready.GraphWriterBatch)
	}
	if ready.GraphWriterBatch.IsolationPreparationID == "" || !graphWriterBatchEffectApplied(ready) || ready.Verification == nil || ready.Review == nil {
		t.Fatalf("isolated aggregate did not pass its single parent effect and native gates: batch=%+v verification=%v review=%v", ready.GraphWriterBatch, ready.Verification != nil, ready.Review != nil)
	}
	if countJournalKind(t, path, "files.intent") != 1 || countJournalKind(t, path, "files.observed") != 1 {
		t.Fatal("isolated cohort did not produce exactly one parent file effect")
	}
	seenIsolation, seenChild := map[string]bool{}, map[string]bool{}
	for _, member := range ready.GraphWriterBatch.Members {
		record, ok := ready.GraphWriterResults[member.TaskID]
		if !ok || record.Isolated == nil || member.IsolationID == "" || member.ChildCandidateID == "" || graphWriterRecordInvocation(record).ID != member.InvocationID {
			t.Fatalf("aggregate member is not bound to actual isolated result: %+v", member)
		}
		if seenIsolation[member.IsolationID] || seenChild[member.ChildCandidateID] {
			t.Fatalf("isolated workspace/candidate was reused: %+v", member)
		}
		seenIsolation[member.IsolationID], seenChild[member.ChildCandidateID] = true, true
		if record.Isolated.Candidate.WorktreeID == workspace.Candidate.WorktreeID || record.Isolated.PreparedChild.Before != record.Isolated.Candidate {
			t.Fatalf("child evidence was not bound to a separate pristine candidate: %+v", member)
		}
	}
	for id, file := range map[string]string{"impl-alpha": "alpha.txt", "impl-beta": "beta.txt", "impl-gamma": "gamma.txt"} {
		if _, ok := ready.Graph.Evidence[id]; !ok {
			t.Fatalf("implementation %s lacks graph completion evidence", id)
		}
		content, err := os.ReadFile(filepath.Join(ready.Workspace.Request.Path, filepath.FromSlash(file)))
		if err != nil || string(content) != "parallel fixture output\n" {
			t.Fatalf("parent did not receive aggregate output %s: %q %v", file, content, err)
		}
	}
	if strings.TrimSpace(ready.Review.Result.Output) == "" {
		t.Fatal("review output missing after isolated aggregate verification")
	}
	assertIsolatedWriterReplayRejectsChangedAggregate(t, path)
}

func TestIsolatedGraphRepairUsesParentAndDoesNotRecreateChildren(t *testing.T) {
	ctx := context.Background()
	c := isolatedThreeWriterCreation(t)
	c.Objective = "isolated parallel repair fixture changes requested"
	if err := os.MkdirAll(c.Config.Codex.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, isolatedThreeWriterGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := StartWorkspace(ctx, path); err != nil {
		t.Fatal(err)
	}
	var err error
	for n := 0; n < 20; n++ {
		s, err = Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		if s.State == "REVIEWING" {
			break
		}
		if s.State != "IMPLEMENTING" {
			t.Fatalf("isolated initial cohort stopped in unexpected state %s", s.State)
		}
		_, progressed, runErr := autonomousGraphImplementing(ctx, path, s)
		if runErr != nil || !progressed {
			t.Fatalf("isolated initial cohort made no progress: %v", runErr)
		}
	}
	s, err = Inspect(path)
	if err != nil || s.State != "REVIEWING" || s.GraphWriterBatch == nil || s.GraphWriterBatch.Version != 2 || len(s.GraphWriterBatch.Members) != 3 {
		t.Fatalf("isolated initial cohort did not reach review: state=%s err=%v", s.State, err)
	}
	initialIsolations := make(map[string]GraphIsolationState, len(s.GraphIsolations))
	for id, isolation := range s.GraphIsolations {
		initialIsolations[id] = isolation
	}
	if len(initialIsolations) != 3 {
		t.Fatalf("expected three confirmed initial writer children, got %d", len(initialIsolations))
	}
	if _, err := RunReview(ctx, path); err != nil {
		t.Fatal(err)
	}
	s, err = Inspect(path)
	var repairVerdict ReviewVerdict
	if s.Review != nil {
		_ = canonical.Decode([]byte(s.Review.Result.Output), &repairVerdict)
	}
	if err != nil || s.State != "REPAIRING" || s.Review == nil || repairVerdict.Decision != "changes_requested" {
		t.Fatalf("fixture did not enter bounded repair from review: state=%s err=%v", s.State, err)
	}
	if s.Candidate == nil {
		t.Fatal("repair transition lost the aggregate parent candidate")
	}
	repairBefore := *s.Candidate
	var repairTaskID string
	for n := 0; n < 20; n++ {
		s, err = autonomousGraphRepairing(ctx, path, s)
		if err != nil {
			t.Fatalf("isolated run could not execute serial parent repair: %v", err)
		}
		s, err = Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range s.Graph.Graph.Tasks {
			if task.Kind == engineeringplan.Implementation && task.ParentID != "" && task.Completed {
				repairTaskID = task.ID
			}
		}
		if repairTaskID != "" {
			break
		}
	}
	if repairTaskID == "" || !autonomousRepairApplied(s) || graphWriterBatchEffectApplied(s) {
		t.Fatalf("serial repair was not applied outside the initial child batch: task=%q repair=%v batchEffect=%v", repairTaskID, autonomousRepairApplied(s), graphWriterBatchEffectApplied(s))
	}
	if s.WriterProposal == nil || s.WriterProposal.Invocation.ID == "" || s.WriterProposal.Prepared.Proposal.Before != repairBefore {
		t.Fatalf("repair did not retain a normal parent-bound writer proposal: present=%v", s.WriterProposal != nil)
	}
	if _, isolatedRepairResult := s.GraphWriterResults[repairTaskID]; isolatedRepairResult {
		t.Fatal("serial repair was incorrectly persisted as an initial isolated graph writer result")
	}
	for id, before := range initialIsolations {
		after, exists := s.GraphIsolations[id]
		if !exists || after.Binding == nil || before.Binding == nil || after.Binding.Request.Path != before.Binding.Request.Path || after.Binding.GitDir != before.Binding.GitDir || after.Candidate == nil || before.Candidate == nil || *after.Candidate != *before.Candidate || after.Outcome != "CONFIRMED" {
			t.Fatalf("initial child binding changed or disappeared during serial repair: %s", id)
		}
	}
	if len(s.GraphIsolations) != len(initialIsolations) || countJournalKind(t, path, "graph.isolation-prepared") != 1 {
		t.Fatal("serial repair recreated or added isolated child worktrees")
	}
	ready, err := RunAutonomous(ctx, path)
	var finalVerdict ReviewVerdict
	if ready.Review != nil {
		_ = canonical.Decode([]byte(ready.Review.Result.Output), &finalVerdict)
	}
	if err != nil || ready.State != "READY" || ready.Candidate == nil || ready.Verification == nil || ready.Review == nil || finalVerdict.Decision != "approve" {
		t.Fatalf("serial parent repair did not pass fresh gates: state=%s err=%v", ready.State, err)
	}
	finalCandidateID, candidateErr := ready.Candidate.ID()
	if candidateErr != nil || ready.Verification.Plan.CandidateID != finalCandidateID || finalVerdict.CandidateID != finalCandidateID {
		t.Fatalf("post-repair native gates are not bound to the current parent candidate: candidate=%s verdict=%s verification=%s err=%v", finalCandidateID, finalVerdict.CandidateID, ready.Verification.Plan.CandidateID, candidateErr)
	}
	if countJournalKind(t, path, "files.intent") != 2 || countJournalKind(t, path, "files.observed") != 2 {
		t.Fatal("expected exactly one initial isolated aggregate and one serial parent repair effect")
	}
}

func isolatedGraphAwaitingApprovalWithGraph(t *testing.T, c Creation, graph engineeringplan.Graph) (string, Snapshot) {
	t.Helper()
	autonomousGitInit(t, c.Repository.Root)
	repo, err := repository.Discover(context.Background(), c.Repository.Root, c.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	c.Repository = repo
	paths, err := controllerstate.Resolve(c.Config.ControllerStateRoot, c.Repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := controllerstate.Initialize(paths, c.Repository); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Runs, 0700); err != nil {
		t.Fatal(err)
	}
	runID, err := canonical.Hash("harness.run.v1", c)
	if err != nil {
		t.Fatal(err)
	}
	path, err := paths.Run(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	invocation, err := plannerInvocationWithContextAndRecipe(c.Config, c.Objective, nil, c.Execution)
	if err != nil {
		t.Fatal(err)
	}
	model := invocation.Profile.Model
	raw, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "plan.recorded", runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, Output: string(raw)}); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if accepted, err := parseAcceptedGraph(s); err != nil || !sameCanonicalGraph(accepted, graph) {
		t.Fatalf("isolated graph fixture rejected: %v", err)
	}
	return path, s
}

func assertIsolatedWriterReplayRejectsChangedAggregate(t *testing.T, path string) {
	t.Helper()
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	batchIndex := -1
	for i, event := range events {
		if event.Kind == "graph.writer.batch-proposed" {
			batchIndex = i
			break
		}
	}
	if batchIndex < 1 {
		t.Fatal("isolated batch event missing from native run")
	}
	before, err := Replay(events[:batchIndex])
	if err != nil {
		t.Fatal(err)
	}
	var batch GraphWriterBatchRecord
	if err := canonical.Decode(events[batchIndex].Payload, &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Prepared.Proposal.Changes) == 0 || batch.Prepared.Proposal.Changes[0].BeforeHash != nil {
		t.Fatal("fixture expected a create-only parent preimage")
	}
	wrongHash := strings.Repeat("a", 64)
	batch.Prepared.Proposal.Changes[0].BeforeHash = &wrongHash
	if err := replayIsolatedGraphWriterBatch(&before, batch); err == nil {
		t.Fatal("isolated replay accepted a substituted parent preimage")
	}
}
