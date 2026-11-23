package control

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/memoryadmission"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
)

// TestGraphMemoryStaleCandidateDoesNotBlockSecondWorker is a deterministic
// regression for duplicate READY candidate handling.
//
// Setup uses the real isolated three-writer preparation fixture and
// buildGraphWriterBatchSpecs (no faked controller state). The injected
// observation is chosen from the actual memory admission policy
// (memoryadmission.Next with configuredMax=3, estimate=1 MiB,
// reserve=1024 MiB): AvailableMiB=1026 gives
// (1026-1024)/1=2 workers capped to 3, so EffectiveWorkers=2 in pressured
// mode. Ample relative to the serial ceiling (1024 MiB -> 1 worker).
//
// Flow: admit specs[0], finish with AdmissionClaimRecorded (releases the
// gate lock via the completion signal but keeps the durable reservation),
// then call BeforeClaim for the SAME spec with a finite context. Per the
// scheduler ClaimAdmissionGate contract (nil callback means the candidate
// became stale and should be re-evaluated on the next Tick so another READY
// sibling can be selected), the duplicate must promptly return (nil, nil)
// without any new observation/grant/release or mutation, leaving the
// original reservation intact.
//
// Current product blocks in the already-granted branch even though only one
// of two effective workers is reserved, so this test FAILS with a context
// timeout. Boundary preserved: UNKNOWN reservations and effective-one
// capacity blocking OTHER tasks remain covered by
// TestUnknownSchedulerClaimRetainsMemoryAdmissionCeiling (unchanged).
func TestGraphMemoryStaleCandidateDoesNotBlockSecondWorker(t *testing.T) {
	c := isolatedThreeWriterCreation(t)
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, isolatedThreeWriterGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Config.Codex.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareIsolatedGraphWriterCohort(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	current, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := graphReadyTasks(current)
	if err != nil {
		t.Fatal(err)
	}
	var implementations []engineeringplan.Task
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			implementations = append(implementations, task)
		}
	}
	specs, err := buildGraphWriterBatchSpecs(path, current, implementations)
	if err != nil || len(specs) < 2 {
		t.Fatalf("writer cohort specs unavailable: count=%d err=%v", len(specs), err)
	}
	gate := newGraphMemoryAdmissionGate(path, func() memoryadmission.Observation {
		return memoryadmission.Observation{Status: memoryadmission.ObservationObserved, Source: memoryadmission.SourceWindowsGlobal, AvailableMiB: 1026, TotalMiB: 4096}
	})
	finish, err := gate.BeforeClaim(context.Background(), specs[0])
	if err != nil || finish == nil {
		t.Fatalf("initial memory reservation failed: callback=%v err=%v", finish != nil, err)
	}
	completion, err := finish(taskscheduler.AdmissionClaimRecorded)
	if err != nil {
		t.Fatal(err)
	}
	if completion != nil {
		completion()
	}
	before, err := Inspect(path)
	if err != nil || before.GraphMemoryAdmission == nil {
		t.Fatalf("admission state missing after first grant: state=%+v err=%v", before.GraphMemoryAdmission, err)
	}
	if before.GraphMemoryAdmission.Decision.EffectiveWorkers != 2 {
		t.Fatalf("fixture did not establish two effective workers (policy: (1026-1024)/1 capped to 3): decision=%+v", before.GraphMemoryAdmission.Decision)
	}
	if len(before.GraphMemoryAdmission.ActiveTaskIDs) != 1 {
		t.Fatalf("expected exactly one active reservation: %+v", before.GraphMemoryAdmission.ActiveTaskIDs)
	}
	beforeActive, ok := before.GraphMemoryAdmission.ActiveTaskIDs[specs[0].ID]
	if !ok {
		t.Fatalf("first reservation lost its task binding: %+v", before.GraphMemoryAdmission.ActiveTaskIDs)
	}
	beforeSequence := before.GraphMemoryAdmission.Sequence
	beforeAdmission := before.GraphMemoryAdmission.LastAdmission

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	duplicateFinish, err := gate.BeforeClaim(ctx, specs[0])
	if err != nil || duplicateFinish != nil {
		t.Fatalf("duplicate READY candidate blocked a second worker: callback=%v err=%v (want nil callback, nil error for stale candidate)", duplicateFinish != nil, err)
	}

	after, err := Inspect(path)
	if err != nil || after.GraphMemoryAdmission == nil {
		t.Fatalf("admission state missing after stale duplicate: state=%+v err=%v", after.GraphMemoryAdmission, err)
	}
	if after.GraphMemoryAdmission.Sequence != beforeSequence || after.GraphMemoryAdmission.LastAdmission != beforeAdmission {
		t.Fatalf("stale duplicate mutated admission sequence: before=(seq=%d id=%q) after=(seq=%d id=%q)", beforeSequence, beforeAdmission, after.GraphMemoryAdmission.Sequence, after.GraphMemoryAdmission.LastAdmission)
	}
	afterActive, ok := after.GraphMemoryAdmission.ActiveTaskIDs[specs[0].ID]
	if !ok || afterActive != beforeActive || len(after.GraphMemoryAdmission.ActiveTaskIDs) != 1 {
		t.Fatalf("stale duplicate changed the original reservation: before=%+v after=%+v", before.GraphMemoryAdmission.ActiveTaskIDs, after.GraphMemoryAdmission.ActiveTaskIDs)
	}
}
