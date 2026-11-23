package control

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func parallelWriterGraphFixture() engineeringplan.Graph {
	return engineeringplan.Graph{Version: engineeringplan.Version, Mode: engineeringplan.ModeGraph, Summary: "two disjoint implementations", Tasks: []engineeringplan.Task{
		{ID: "impl-alpha", Kind: engineeringplan.Implementation, Title: "Create alpha output", ScopePaths: []string{"."}, WritePaths: []string{"alpha.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "alpha output exists"}}, EstimatedSeconds: 30},
		{ID: "impl-beta", Kind: engineeringplan.Implementation, Title: "Create beta output", ScopePaths: []string{"."}, WritePaths: []string{"beta.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "beta output exists"}}, EstimatedSeconds: 30},
		{ID: "verify", Kind: engineeringplan.Verification, Title: "Run native checks", Dependencies: []string{"impl-alpha", "impl-beta"}, ScopePaths: []string{"."}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "check", Description: "configured checks pass"}}, EstimatedSeconds: 30},
		{ID: "review", Kind: engineeringplan.Review, Title: "Review candidate", Dependencies: []string{"verify"}, ScopePaths: []string{"."}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "review", Description: "approve the verified candidate"}}, EstimatedSeconds: 30},
	}}
}

func parallelWriterCreation(t *testing.T, maxParallel int) Creation {
	t.Helper()
	c := graphCreation(t, maxParallel)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := fixtureFileHash(executable)
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(t.TempDir(), "codex-state")
	auth := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(auth, []byte(`{"tokens":{"access_token":"fixture-token","account_id":"fixture-account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	writer := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "writer-fixture", Effort: "high", Role: "writer"}
	explorer := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "explorer-fixture", Effort: "low", Role: "explorer"}
	reviewer := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "reviewer-fixture", Effort: "high", Role: "reviewer"}
	c.Config.Writer = &writer
	c.Config.Explorer = &explorer
	c.Config.Reviewer = &reviewer
	c.Config.WriterContract = "anchored-edits-v1"
	c.Config.ExplorerContract = "json-v2"
	c.Config.PlannerContract = plannerContractGraphV4
	c.Config.Codex = &config.Codex{Executable: executable, ExecutableHash: hash, StateRoot: stateRoot, AuthSource: auth}
	c.Execution.RepairPlanningVersion = 1
	c.Execution.ParallelImplementationVersion = 1
	return c
}

func TestParallelGraphWritersAggregateOnceAndReachReady(t *testing.T) {
	assertParallelGraphWriters(t, parallelWriterCreation(t, 2), true)
}

func TestStaticGraphWriterProbeBeforeHostIntentIsReadOnly(t *testing.T) {
	controllerPath, snapshot := graphAwaitingApprovalWithGraph(t, parallelWriterCreation(t, 2), parallelWriterGraphFixture())
	var err error
	snapshot, err = Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	machineAuthorizePlan(t, controllerPath, snapshot)
	snapshot, err = Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = ensureGraphRecorded(controllerPath, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), controllerPath); err != nil {
		t.Fatal(err)
	}
	snapshot, err = Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := graphReadyTasks(snapshot)
	if err != nil || len(ready) == 0 {
		t.Fatalf("graph writer was not ready: tasks=%d err=%v", len(ready), err)
	}
	question, err := graphWriterTaskQuestion(snapshot, ready[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := maybeAdmitTaskContext(context.Background(), controllerPath, "writer", question); err != nil {
		t.Fatal(err)
	}
	snapshot, err = Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	ready, err = graphReadyTasks(snapshot)
	if err != nil || len(ready) == 0 {
		t.Fatalf("graph writer was not ready after context admission: tasks=%d err=%v", len(ready), err)
	}
	specs, err := buildGraphWriterBatchSpecs(controllerPath, snapshot, ready[:1])
	if err != nil || len(specs) != 1 {
		t.Fatalf("graph writer task could not be bound: specs=%d err=%v", len(specs), err)
	}
	expected, err := expectedWriterHostForTask(snapshot, specs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(expected.Launch.Root, "writer.jsonl")
	if _, err := os.Stat(runtimePath); !os.IsNotExist(err) {
		t.Fatalf("fixture unexpectedly has a writer runtime journal before intent: %v", err)
	}
	evidence, err := (ScheduledDispatchAdapter{}).Probe(context.Background(), taskscheduler.ProbeRequest{Task: specs[0]})
	if err != nil || evidence.Status != taskscheduler.StatusReady || evidence.AdmissionID != "" {
		t.Fatalf("initial static writer probe was not ready without an intent: evidence=%+v err=%v", evidence, err)
	}
	after, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.GraphWriterHosts) != 0 {
		t.Fatalf("read-only probe created a graph writer host intent: %d", len(after.GraphWriterHosts))
	}
	if _, err := os.Stat(runtimePath); !os.IsNotExist(err) {
		t.Fatalf("read-only probe created a writer runtime journal: %v", err)
	}
}

func TestParallelPolicySerialWorkerUsesOneAggregate(t *testing.T) {
	assertParallelGraphWriters(t, parallelWriterCreation(t, 1), false)
}

func TestParallelGraphWritersV2AccessAndUsage(t *testing.T) {
	assertParallelGraphWriters(t, parallelWriterV2Creation(t, 2), false)
}

func TestGraphWriterBatchSortsAggregateChangesByPath(t *testing.T) {
	filesHash, err := worktree.FilesID([]worktree.FileState{})
	if err != nil {
		t.Fatal(err)
	}
	before := worktree.Candidate{
		Version: 1, WorktreeID: strings.Repeat("a", 64), Head: strings.Repeat("b", 40),
		IndexHash: strings.Repeat("c", 64), FilesHash: filesHash,
	}
	graph := parallelWriterGraphFixture()
	graph.Tasks[0].ID, graph.Tasks[0].WritePaths = "impl-alpha", []string{"zeta.txt"}
	graph.Tasks[1].ID, graph.Tasks[1].WritePaths = "impl-zulu", []string{"alpha.txt"}
	graph.Tasks[2].Dependencies = []string{"impl-alpha", "impl-zulu"}
	contents := func(value string) *string {
		encoded := base64.StdEncoding.EncodeToString([]byte(value))
		return &encoded
	}
	proposal := func(path, value string) fileeffects.Proposal {
		return fileeffects.Proposal{Version: 1, Nonce: "writer-" + path, Before: before, BeforeFiles: []worktree.FileState{}, Changes: []fileeffects.Change{{Path: path, ContentBase64: contents(value)}}}
	}
	c := creation(t)
	s := Snapshot{
		RunID: strings.Repeat("1", 64), PlanID: strings.Repeat("2", 64), Creation: c,
		Workspace: &worktree.Binding{}, Candidate: &before,
		Graph: &GraphState{Graph: graph, Digest: strings.Repeat("d", 64), Revision: 1},
		GraphWriterResults: map[string]GraphWriterRecord{
			"impl-alpha": {TaskID: "impl-alpha", Writer: WriterRecord{Invocation: runtime.Invocation{ID: strings.Repeat("3", 64)}, Prepared: PreparedFiles{Proposal: proposal("zeta.txt", "zeta\\n")}}},
			"impl-zulu":  {TaskID: "impl-zulu", Writer: WriterRecord{Invocation: runtime.Invocation{ID: strings.Repeat("4", 64)}, Prepared: PreparedFiles{Proposal: proposal("alpha.txt", "alpha\\n")}}},
		},
	}
	batch, err := buildGraphWriterBatch(s, []string{"impl-zulu", "impl-alpha"})
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{batch.Prepared.Proposal.Changes[0].Path, batch.Prepared.Proposal.Changes[1].Path}
	if got, want := strings.Join(paths, ","), "alpha.txt,zeta.txt"; got != want {
		t.Fatalf("aggregate changes not sorted by path: got %s want %s", got, want)
	}
}
func TestGraphWriterUnknownHostBlocksAutomaticResend(t *testing.T) {
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "fixture", Effort: "high", Role: "writer"}, "task-bound writer")
	if err != nil {
		t.Fatal(err)
	}
	s := Snapshot{GraphWriterHosts: map[string]WriterHostState{
		"impl-alpha": {Intent: WriterHostIntent{Invocation: invocation}},
	}}
	if err := autonomousGraphWriterDispatchBlocked(s, "controller", "impl-beta"); err == nil || !strings.Contains(err.Error(), "impl-alpha") {
		t.Fatalf("unresolved graph writer was not blocked from resend: %v", err)
	}
}

func TestGraphWriterDispatchTimingLegacyOptionalAndOrdered(t *testing.T) {
	var legacy *GraphWriterDispatchTiming
	if err := legacy.validate(); err != nil {
		t.Fatalf("legacy record without dispatch timing rejected: %v", err)
	}
	start := time.Now().UTC()
	if err := (&GraphWriterDispatchTiming{StartedAt: start.Format(time.RFC3339Nano), EndedAt: start.Add(time.Millisecond).Format(time.RFC3339Nano)}).validate(); err != nil {
		t.Fatalf("ordered UTC dispatch timing rejected: %v", err)
	}
	if err := (&GraphWriterDispatchTiming{StartedAt: start.Format(time.RFC3339Nano), EndedAt: start.Add(-time.Millisecond).Format(time.RFC3339Nano)}).validate(); err == nil {
		t.Fatal("reversed dispatch timing accepted")
	}
	local := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.FixedZone("plus-one", 3600))
	if err := (&GraphWriterDispatchTiming{StartedAt: local.Format(time.RFC3339Nano), EndedAt: local.Add(time.Millisecond).Format(time.RFC3339Nano)}).validate(); err == nil {
		t.Fatal("non-UTC dispatch timing accepted")
	}
	if timing := observedGraphWriterDispatchTiming(start.Add(time.Millisecond), start); timing != nil {
		t.Fatalf("reversed local clock observation was persisted: %+v", timing)
	}
	// The caller can still append the otherwise successful proposal without
	// auxiliary timing when the local clock observation is invalid.
	encoded, err := canonical.Bytes(GraphWriterRecord{TaskID: "impl-alpha", Dispatch: observedGraphWriterDispatchTiming(start.Add(time.Millisecond), start)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "dispatch") {
		t.Fatalf("clock-invalid timing was serialized into proposal: %s", encoded)
	}
}

func parallelWriterV2Creation(t *testing.T, maxParallel int) Creation {
	t.Helper()
	c := codexAccessCreation(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := fixtureFileHash(executable)
	if err != nil {
		t.Fatal(err)
	}
	c.Config.Codex.Executable = executable
	c.Config.Codex.ExecutableHash = hash
	c.Config.Codex.UsageQualified = true // The app-server fixture emits bounded, identity-bound usage.
	if err := os.WriteFile(c.Config.Codex.AuthSource, []byte(`{"tokens":{"access_token":"fixture-token","account_id":"fixture-account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c.Config.Planner = runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixture-planner", Effort: "none", Role: "planner"}
	writer := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "writer-fixture", Effort: "high", Role: "writer"}
	explorer := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "explorer-fixture", Effort: "low", Role: "explorer"}
	reviewer := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "reviewer-fixture", Effort: "high", Role: "reviewer"}
	c.Config.Writer = &writer
	c.Config.Explorer = &explorer
	c.Config.Reviewer = &reviewer
	c.Config.WriterContract = "anchored-edits-v1"
	c.Config.ExplorerContract = "json-v2"
	c.Config.PlannerContract = plannerContractGraphV4
	c.Config.Access.Limits.Concurrency = 2
	c.Config.Access.Profiles = append(c.Config.Access.Profiles, access.Profile{Version: 1, Name: "fixture-planner", Kind: "subscription", Runtime: "fake", Provider: "deterministic", AuthMode: "fixture-session", RepositoryClasses: []access.Class{access.Private}})
	c.Config.Access.Roles["planner"] = "fixture-planner"
	c.Config.Access.Roles["writer"] = "chatgpt"
	c.Config.Access.Roles["explorer"] = "chatgpt"
	c.Config.Access.Roles["reviewer"] = "chatgpt"
	c.Config.Access.Invocations["writer"] = config.InvocationLimit{Tokens: 100}
	c.Config.Access.Invocations["explorer"] = config.InvocationLimit{Tokens: 100}
	c.Config.Access.Invocations["reviewer"] = config.InvocationLimit{Tokens: 100}
	c.Config.Access.Invocations["planner"] = config.InvocationLimit{Tokens: 100}
	c.Config.Verification = []config.Check{{Name: "unit", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}}
	c.Config.Repository = "fixture"
	c.Execution = graphPolicy(maxParallel)
	c.Execution.RepairPlanningVersion = 1
	c.Execution.ParallelImplementationVersion = 1
	if err := c.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func assertParallelGraphWriters(t *testing.T, c Creation, overlap bool) {
	t.Helper()
	graphPath, _ := graphAwaitingApprovalWithGraph(t, c, parallelWriterGraphFixture())
	s, err := Inspect(graphPath)
	if err != nil {
		t.Fatal(err)
	}
	machineAuthorizePlan(t, graphPath, s)
	workspace, err := StartWorkspace(context.Background(), graphPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Config.Codex.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if overlap {
		if err := os.WriteFile(filepath.Join(c.Config.Codex.StateRoot, "graph-writer-barrier"), []byte("enabled"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if workspace.Candidate == nil {
		t.Fatal("workspace fixture did not bind candidate")
	}

	ready, err := RunAutonomous(context.Background(), graphPath)
	if err != nil {
		for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("parallel graph failure cause: %v", cause)
		}
		schedules, _ := filepath.Glob(graphPath + ".graph-writers-*.jsonl")
		for _, schedulePath := range schedules {
			if schedule, scheduleErr := taskscheduler.Inspect(schedulePath); scheduleErr == nil {
				for id, taskState := range schedule.Tasks {
					t.Logf("writer schedule task %s: status=%s generation=%d reason=%s", id, taskState.Status, taskState.Generation, taskState.ParkReason)
				}
			}
			if events, eventErr := journal.Read(schedulePath); eventErr == nil {
				type claimedEvent struct {
					Claim   taskscheduler.Claim `json:"claim"`
					ClaimID string              `json:"claim_id"`
				}
				for i := len(events) - 1; i >= 0; i-- {
					if events[i].Kind != "task.claimed" {
						continue
					}
					var claimed claimedEvent
					if canonical.Decode(events[i].Payload, &claimed) == nil && claimed.Claim.Task.ID == "impl-beta" {
						deltaErr := verifyGraphWriterCohortDelta(graphPath, claimed.Claim)
						t.Logf("latest beta claim static=%v delta=%v", isStaticGraphWriterCohort(ready, claimed.Claim), deltaErr)
						break
					}
				}
			}
		}
		t.Fatalf("parallel graph run: %v (state=%s graphHosts=%d graphResults=%d)", err, ready.State, len(ready.GraphWriterHosts), len(ready.GraphWriterResults))
	}
	if ready.State != "READY" || ready.GraphWriterBatch == nil || len(ready.GraphWriterResults) != 2 {
		t.Fatalf("parallel run did not reach READY with two actual proposals: state=%s batch=%+v writers=%d", ready.State, ready.GraphWriterBatch, len(ready.GraphWriterResults))
	}
	if autonomousRepairApplied(ready) {
		t.Fatal("initial aggregate was misclassified as a serial repair")
	}
	if overlap {
		for _, id := range []string{"impl-alpha", "impl-beta"} {
			if _, err := os.Stat(filepath.Join(c.Config.Codex.StateRoot, "ready-"+id)); err != nil {
				t.Fatalf("writer %s never overlapped at app-server turn start: %v", id, err)
			}
		}
	}
	for _, id := range []string{"impl-alpha", "impl-beta"} {
		if _, ok := ready.Graph.Evidence[id]; !ok {
			t.Fatalf("task %s lacks graph completion evidence", id)
		}
		host := ready.GraphWriterHosts[id]
		record := ready.GraphWriterResults[id]
		if record.Dispatch == nil || record.Dispatch.validate() != nil {
			t.Fatalf("writer %s lacks ordered observed wrapper timing: %+v", id, record.Dispatch)
		}
		methods, err := os.ReadFile(filepath.Join(host.Intent.Launch.Root, "home", "fixture-methods.log"))
		if err != nil || strings.Count(string(methods), "turn/start\n") != 1 {
			t.Fatalf("writer %s did not retain exactly one app-server turn: methods=%q err=%v", id, methods, err)
		}
	}
	alpha, beta := ready.GraphWriterResults["impl-alpha"].Dispatch, ready.GraphWriterResults["impl-beta"].Dispatch
	alphaStart, _ := time.Parse(time.RFC3339Nano, alpha.StartedAt)
	alphaEnd, _ := time.Parse(time.RFC3339Nano, alpha.EndedAt)
	betaStart, _ := time.Parse(time.RFC3339Nano, beta.StartedAt)
	betaEnd, _ := time.Parse(time.RFC3339Nano, beta.EndedAt)
	intervalsOverlap := alphaStart.Before(betaEnd) && betaStart.Before(alphaEnd)
	if overlap && !intervalsOverlap {
		t.Fatalf("barrier fixture did not overlap writer dispatches: alpha=%+v beta=%+v", alpha, beta)
	}
	if c.Execution.MaxParallel == 1 && intervalsOverlap {
		t.Fatalf("serial worker overlapped writer dispatches: alpha=%+v beta=%+v", alpha, beta)
	}
	if countJournalKind(t, graphPath, "files.intent") != 1 || countJournalKind(t, graphPath, "files.observed") != 1 {
		t.Fatal("parallel proposals did not produce exactly one aggregate file effect")
	}
	if ready.Verification == nil || ready.Review == nil {
		t.Fatal("READY state lacks fresh native verification and review records")
	}
	if alpha, err := os.ReadFile(filepath.Join(ready.Workspace.Request.Path, "alpha.txt")); err != nil || string(alpha) != "parallel fixture output\n" {
		t.Fatalf("alpha aggregate change missing: %q %v", alpha, err)
	}
	if beta, err := os.ReadFile(filepath.Join(ready.Workspace.Request.Path, "beta.txt")); err != nil || string(beta) != "parallel fixture output\n" {
		t.Fatalf("beta aggregate change missing: %q %v", beta, err)
	}
	if strings.TrimSpace(ready.Review.Result.Output) == "" {
		t.Fatal("reviewer result missing")
	}
	if c.Config.Version == 2 {
		usage, err := MeasureRunUsage(graphPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(usage.Invocations) != 3 {
			t.Fatalf("graph writer model-access usage omitted member/reviewer invocations: %+v", usage.Invocations)
		}
		for _, entry := range usage.Invocations {
			if !entry.ReceiptMatched || entry.Usage == nil || !entry.Usage.Completed {
				t.Fatalf("graph invocation usage lacks terminal receipt: %+v", entry)
			}
		}
	}
}
