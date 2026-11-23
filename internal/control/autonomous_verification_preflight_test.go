package control

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/access"
)

func TestPlanningRestartPreflightBeforeFirstEffect(t *testing.T) {
	for _, graph := range []bool{false, true} {
		name := "legacy"
		if graph {
			name = "graph"
		}
		t.Run(name, func(t *testing.T) {
			c := autonomousCreation(t, 1)
			if graph {
				c = graphCreation(t, 1)
			}
			c.Config.Verification[0].Argv = []string{"fabric-test-missing-on-planning-restart-48219"}
			path := filepath.Join(t.TempDir(), "run.jsonl")
			if err := Append(path, "run.created", c); err != nil {
				t.Fatal(err)
			}
			if err := Append(path, "planning.started", struct{}{}); err != nil {
				t.Fatal(err)
			}
			before, err := Inspect(path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = RunAutonomous(context.Background(), path)
			if !errors.Is(err, ErrAutonomousVerificationNotRun) {
				t.Fatalf("restart bypassed first-call preflight: %v", err)
			}
			after, err := Inspect(path)
			if err != nil || after.State != "PLANNING" || after.ControllerHead != before.ControllerHead {
				t.Fatalf("restart preflight changed journal: %+v %v", after, err)
			}
			// An already-admitted effect uses recovery, never new-call preflight.
			admitted := before
			admitted.PlannerAccess = &access.Intent{}
			if err := preflightInitialVerification(admitted); err != nil {
				t.Fatalf("preflight obstructed admitted effect recovery: %v", err)
			}
		})
	}
}

func TestInitialVerificationPreflightStopsBeforePlanning(t *testing.T) {
	for _, graph := range []bool{false, true} {
		name := "legacy"
		if graph {
			name = "graph"
		}
		t.Run(name, func(t *testing.T) {
			c := autonomousCreation(t, 1)
			if graph {
				c = graphCreation(t, 1)
			}
			c.Config.Verification[0].Argv = []string{"fabric-test-missing-before-planning-48219"}
			path := filepath.Join(t.TempDir(), "run.jsonl")
			if err := Append(path, "run.created", c); err != nil {
				t.Fatal(err)
			}
			before, err := Inspect(path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = RunAutonomous(context.Background(), path)
			if !errors.Is(err, ErrAutonomousVerificationNotRun) {
				t.Fatalf("expected verification preflight stop: %v", err)
			}
			after, err := Inspect(path)
			if err != nil {
				t.Fatal(err)
			}
			if after.State != "OBJECTIVE" || after.ControllerHead != before.ControllerHead {
				t.Fatalf("preflight admitted planning or changed journal: %+v", after)
			}
		})
	}
}

func TestAutonomousMissingVerificationExecutableStopsBeforeRoleEffects(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		c := autonomousCreation(t, 2)
		c.Config.Verification[0].Name = "required-go-check"
		c.Config.Verification[0].Argv = []string{"fabric-test-missing-go-executable-48219"}
		path, s := autonomousAwaitingApproval(t, c)
		machineAuthorizePlan(t, path, s)

		result, err := RunAutonomous(context.Background(), path)
		if err == nil || !errors.Is(err, ErrAutonomousVerificationNotRun) {
			t.Fatalf("missing required executable did not produce a bounded preflight error: %v", err)
		}
		latest, inspectErr := Inspect(path)
		if inspectErr != nil {
			t.Fatal(inspectErr)
		}
		if result.State != "IMPLEMENTING" || latest.WorkspaceOutcome != "CONFIRMED" || latest.ExplorerHost != nil || latest.WriterHost != nil || latest.FileIntent != nil || latest.RepairAttempts != 0 {
			t.Fatalf("missing executable reached role or file work: result=%+v snapshot=%+v", result, latest)
		}
		for _, kind := range []string{"explorer.host-intent", "writer.host-intent", "files.intent", "autonomous.repair-started"} {
			if countJournalKind(t, path, kind) != 0 {
				t.Fatalf("missing executable produced %s", kind)
			}
		}
	})

	t.Run("graph", func(t *testing.T) {
		c := graphCreation(t, 1)
		c.Config.Verification[0].Name = "required-go-check"
		c.Config.Verification[0].Argv = []string{"fabric-test-missing-go-executable-48219"}
		path, s := graphAwaitingApproval(t, c)
		machineAuthorizePlan(t, path, s)

		result, err := RunAutonomous(context.Background(), path)
		if err == nil || !errors.Is(err, ErrAutonomousVerificationNotRun) {
			t.Fatalf("missing required executable did not produce a bounded preflight error: %v", err)
		}
		latest, inspectErr := Inspect(path)
		if inspectErr != nil {
			t.Fatal(inspectErr)
		}
		if result.State != "IMPLEMENTING" || latest.WorkspaceOutcome != "CONFIRMED" || len(latest.ExplorerRuns) != 0 || len(latest.GraphWriterHosts) != 0 || latest.WriterHost != nil || latest.FileIntent != nil || latest.RepairAttempts != 0 {
			t.Fatalf("missing executable reached graph role or file work: result=%+v snapshot=%+v", result, latest)
		}
		for _, kind := range []string{"explorer.host-intent", "graph.writer.host-intent", "writer.host-intent", "files.intent", "autonomous.repair-started"} {
			if countJournalKind(t, path, kind) != 0 {
				t.Fatalf("missing executable produced %s", kind)
			}
		}
	})
}

func TestUnstartedVerificationDoesNotConsumeRepairButStartedFailureDoes(t *testing.T) {
	t.Run("not-run", func(t *testing.T) {
		c := autonomousCreation(t, 2)
		c.Config.Verification[0].Name = "required-go-check"
		c.Config.Verification[0].Argv = []string{"fabric-test-missing-go-executable-48219"}
		path, s := autonomousAwaitingApproval(t, c)
		machineAuthorizePlan(t, path, s)
		applyWriterOutput(t, path, "writer output\n")
		failed, err := Verify(context.Background(), path)
		if err != nil || failed.State != "REPAIRING" || failed.Verification == nil || len(failed.Verification.Observations) != 1 {
			t.Fatalf("fixture did not persist the unstarted native result: %+v %v", failed, err)
		}
		observation := failed.Verification.Observations[0].Result
		if observation.Started || observation.Status != "NOT_RUN" {
			t.Fatalf("fixture result is not unstarted NOT_RUN: %+v", observation)
		}
		beforeRepair := countJournalKind(t, path, "autonomous.repair-started")
		beforeWriter := countJournalKind(t, path, "writer.host-intent")
		_, err = RunAutonomous(context.Background(), path)
		if err == nil || !errors.Is(err, ErrAutonomousVerificationNotRun) {
			t.Fatalf("unstarted check was treated as semantic failure: %v", err)
		}
		if countJournalKind(t, path, "autonomous.repair-started") != beforeRepair || countJournalKind(t, path, "writer.host-intent") != beforeWriter {
			t.Fatal("unstarted check consumed a repair slot or dispatched another writer")
		}
	})

	t.Run("started-failure", func(t *testing.T) {
		path, failed := repairingAutonomousFixture(t, 1)
		if failed.Verification == nil || len(failed.Verification.Observations) != 1 {
			t.Fatal("failure fixture lacks a native observation")
		}
		observation := failed.Verification.Observations[0].Result
		if !observation.Started || observation.Status != "FAIL" {
			t.Fatalf("fixture result is not an actual native failure: %+v", observation)
		}
		if err := rejectUnstartedAutonomousVerification(failed); err != nil {
			t.Fatalf("started native failure was blocked as NOT_RUN: %v", err)
		}
		_, err := RunAutonomous(context.Background(), path)
		if countJournalKind(t, path, "autonomous.repair-started") != 1 {
			t.Fatalf("actual native failure did not remain eligible for repair (run error %v)", err)
		}
	})
}
