package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexrpc"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/codexusage"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestSemanticUsagePendingKeepsReservationAndBlocksNextInvocation(t *testing.T) {
	path, snapshot, invocation := qualifiedPlannerAccess(t)
	runtimePath := filepath.Join(t.TempDir(), "planner.jsonl")
	result, head := appendSemanticUsagePendingRuntime(t, runtimePath, snapshot, invocation, true)

	updated, observed, pending, err := observeSemanticUsagePending(context.Background(), path, runtimePath, invocation, "harness.planner-result.v1")
	if err != nil {
		t.Fatal(err)
	}
	wantHash, _ := canonical.Hash("harness.planner-result.v1", result)
	if observed.Output != result.Output || pending.RuntimeJournalHead != head || pending.ResultHash != wantHash {
		t.Fatal("pending proof did not retain the exact completed result", pending)
	}
	if err := requireModelAccessResult(updated, invocation, head, wantHash, pending.ThreadID, pending.TurnID, true); err != nil {
		t.Fatalf("exact pending result did not satisfy its controller receipt: %v", err)
	}
	if err := requireModelAccessResult(updated, invocation, head, "different-result-hash", pending.ThreadID, pending.TurnID, true); !errors.Is(err, ErrSemanticUsagePending) {
		t.Fatalf("substituted pending result was accepted: %v", err)
	}
	index := modelAccessIndex(updated, invocation.ID)
	if index < 0 || updated.ModelAccess[index].SemanticPending == nil || updated.ModelAccess[index].Terminal != nil {
		t.Fatal("pending accounting was falsely settled", updated.ModelAccess)
	}
	policy, err := updated.Creation.Config.AccessPolicy(updated.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := access.RequireActive(modelAccessJournal(path), policy, updated.ModelAccess[index].Intent); err != nil {
		t.Fatalf("known completed semantic output released its reservation: %v", err)
	}

	other, err := runtime.NewInvocation(invocation.Profile, "a later planner request")
	if err != nil {
		t.Fatal(err)
	}
	if err := requireModelAccessAdmissionAvailable(updated, other); !errors.Is(err, ErrSemanticUsagePending) {
		t.Fatalf("next provider admission was not blocked by unresolved usage: %v", err)
	}
	if err := requireModelAccessAdmissionAvailable(updated, invocation); err != nil {
		t.Fatalf("exact retained invocation could not be read back: %v", err)
	}
	local := invocation
	local.Profile.Runtime = "fake"
	if err := requireModelAccessAdmissionAvailable(updated, local); err != nil {
		t.Fatalf("pending usage incorrectly blocked local fixture work: %v", err)
	}
	before, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureModelAccessIntent(path, updated, other); !errors.Is(err, ErrSemanticUsagePending) {
		t.Fatalf("new model access intent bypassed pending usage: %v", err)
	}
	after, err := journal.Read(path)
	if err != nil || len(after) != len(before) {
		t.Fatalf("blocked admission changed controller history: before=%d after=%d err=%v", len(before), len(after), err)
	}
}

func TestSemanticUsagePendingProofRejectsSubstitutionAndPublicAppend(t *testing.T) {
	path, snapshot, invocation := qualifiedPlannerAccess(t)
	runtimePath := filepath.Join(t.TempDir(), "planner.jsonl")
	appendSemanticUsagePendingRuntime(t, runtimePath, snapshot, invocation, true)
	updated, _, pending, err := observeSemanticUsagePending(context.Background(), path, runtimePath, invocation, "harness.planner-result.v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateModelAccessSemanticPending(updated, invocation, pending); err != nil {
		t.Fatal("valid pending proof did not validate", err)
	}
	wrongDomain := pending
	wrongDomain.ResultDomain = "harness.review-result.v1"
	if err := validateModelAccessSemanticPending(updated, invocation, wrongDomain); err == nil {
		t.Fatal("substituted result domain passed proof validation")
	}
	wrongCandidate := pending
	wrongCandidate.CandidateBinding = &codexruntime.CandidateBinding{}
	if err := validateModelAccessSemanticPending(updated, invocation, wrongCandidate); err == nil {
		t.Fatal("substituted candidate binding passed proof validation")
	}
	unqualified := updated
	unqualified.Creation.Config.Codex.UsageQualified = false
	if err := validateModelAccessSemanticPending(unqualified, invocation, pending); err == nil {
		t.Fatal("controller policy without qualified required usage accepted pending proof")
	}
	if err := Append(path, "model.access-semantic-pending", ModelAccessSemanticPending{}); err == nil {
		t.Fatal("public append accepted a controller-authored pending proof")
	}
}

func TestAutonomousResumeMaterializesCrashWindowSemanticResult(t *testing.T) {
	path, snapshot, invocation := qualifiedPlannerAccess(t)
	launch, err := expectedPlannerHost(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.host-intent", launch); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.host-ready", launch); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(launch.Root, 0700); err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(launch.Root, "planner.jsonl")
	_, runtimeHead := appendSemanticUsagePendingRuntime(t, runtimePath, snapshot, invocation, false)

	before, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	controllerEventsBefore, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := autonomousDispatchBlocked(before); err == nil {
		t.Fatal("unmaterialized access intent unexpectedly passed dispatch preflight")
	}
	updated, err := reconcileUnrecordedSemanticUsagePending(context.Background(), path, before)
	if err != nil {
		t.Fatal(err)
	}
	index := modelAccessIndex(updated, invocation.ID)
	if index < 0 || updated.ModelAccess[index].SemanticPending == nil || updated.ModelAccess[index].Terminal != nil {
		t.Fatal("known retained result was not materialized after restart")
	}
	if err := autonomousDispatchBlocked(updated); err != nil {
		t.Fatalf("exact completed semantic result remained an UNKNOWN dispatch blocker: %v", err)
	}
	controllerEventsAfter, err := journal.Read(path)
	if err != nil || len(controllerEventsAfter) != len(controllerEventsBefore)+1 || controllerEventsAfter[len(controllerEventsAfter)-1].Kind != "model.access-semantic-pending" {
		t.Fatalf("restart did more than materialize the pending proof: before=%d after=%d err=%v", len(controllerEventsBefore), len(controllerEventsAfter), err)
	}
	_, runtimeHeadAfter, err := codexruntime.InspectWithHead(runtimePath)
	if err != nil || runtimeHeadAfter != runtimeHead {
		t.Fatalf("offline reconciliation changed runtime/provider history: before=%s after=%s err=%v", runtimeHead, runtimeHeadAfter, err)
	}
}

func qualifiedPlannerAccess(t *testing.T) (string, Snapshot, runtime.Invocation) {
	t.Helper()
	created := codexAccessCreation(t)
	created.Config.Codex.UsageQualified = true
	if err := created.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", created); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	i, err := runtime.NewInvocation(s.Creation.Config.Planner, s.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	s, _, err = ensureModelAccessIntent(path, s, i)
	if err != nil {
		t.Fatal(err)
	}
	return path, s, i
}

func appendSemanticUsagePendingRuntime(t *testing.T, path string, s Snapshot, invocation runtime.Invocation, usageBlocked bool) (runtime.Result, string) {
	t.Helper()
	directory := filepath.Join(filepath.Dir(path), "workspace")
	threadID, turnID := "thread-semantic-pending", "turn-semantic-pending"
	effort, model, provider := invocation.Profile.Effort, invocation.Profile.Model, invocation.Profile.Provider
	appendUnchecked(t, path, "runtime.intent", codexruntime.Intent{Invocation: invocation, Directory: directory})
	appendUnchecked(t, path, "runtime.source", s.Creation.Repository)
	appendUnchecked(t, path, "runtime.thread", codexrpc.ThreadSettings{
		ThreadID: threadID, Model: model, Provider: provider, Effort: &effort,
		Directory: directory, Approval: "never", Sandbox: "readOnly", Network: false,
	})
	budget, requireLive, qualified, unlimited, err := codexRuntimeUsagePolicy(s, invocation.Profile.Role)
	if err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	appendUnchecked(t, path, "runtime.usage-baseline", codexruntime.UsagePolicy{
		Version: 1, UnlimitedTokens: unlimited, ThreadID: threadID,
		Baseline: codexusage.TokenUsage{CacheWriteInputTokens: &zero}, Budget: budget,
		Required: requireLive || budget > 0, Qualified: qualified, Origin: "FRESH_THREAD",
	})
	appendUnchecked(t, path, "runtime.turn-intent", struct {
		InvocationID string `json:"invocation_id"`
	}{invocation.ID})
	appendUnchecked(t, path, "runtime.turn", struct {
		ID string `json:"id"`
	}{turnID})
	appendUnchecked(t, path, "runtime.usage-start", codexruntime.UsageStart{TurnID: turnID})
	appendUnchecked(t, path, "runtime.stream-terminal", codexruntime.TurnStatus{ID: turnID, Status: "completed"})
	appendUnchecked(t, path, "runtime.turn-status", codexruntime.TurnStatus{ID: turnID, Status: "completed"})
	result := runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile,
		ObservedModel: &model, ObservedProvider: &provider, ObservedEffort: &effort, Output: `{"plan":"known result"}`}
	appendUnchecked(t, path, "runtime.semantic-result", codexruntime.SemanticTurnResult{TurnID: turnID, Result: result})
	if usageBlocked {
		appendUnchecked(t, path, "runtime.usage-blocked", codexruntime.UsageStop{Reason: "USAGE_MISSING", ThreadID: threadID, TurnID: turnID})
	}
	head, err := journal.Read(path)
	if err != nil || len(head) == 0 {
		t.Fatalf("runtime fixture journal missing: %v", err)
	}
	return result, head[len(head)-1].Hash
}
