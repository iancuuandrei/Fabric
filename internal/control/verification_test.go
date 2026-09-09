package control

import (
	"context"
	"os"
	"reflect"
	"testing"

	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/verification"
	"harness.local/engorch/internal/worktree"
)

func TestVerificationControllerChild(t *testing.T) {
	if len(os.Args) < 2 {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "child-pass":
		os.Exit(0)
	case "child-fail":
		os.Exit(7)
	case "child-mutate":
		if err := os.WriteFile("file.txt", []byte("changed by check"), 0600); err != nil {
			os.Exit(8)
		}
		os.Exit(0)
	}
}

func verificationFixture(t *testing.T) (string, Snapshot, verification.Plan, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argv := []string{exe, "-test.run=^TestVerificationControllerChild$", "--", "child-pass"}
	p, _ := approvedRepository(t,
		config.Check{Name: "first", Argv: argv, TimeoutSeconds: 10},
		config.Check{Name: "second", Argv: argv, TimeoutSeconds: 10})
	s, err := StartWorkspace(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	candidateID, _ := s.Candidate.ID()
	plan, err := verification.PreparePlan(s.RunID, "interruption-fixture", candidateID, s.Workspace.Request.Path, s.Creation.Config.Verification)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(p, "verification.planned", plan); err != nil {
		t.Fatal(err)
	}
	planID, _ := plan.ID()
	return p, s, plan, planID
}

func TestVerificationResumesOnlyUnstartedChecks(t *testing.T) {
	p, s, plan, planID := verificationFixture(t)
	start := VerificationStart{planID, 0}
	if err := Append(p, "verification.started", start); err != nil {
		t.Fatal(err)
	}
	result, err := verification.Execute(context.Background(), plan.Invocations[0])
	if err != nil {
		t.Fatal(err)
	}
	current, err := worktree.Fingerprint(context.Background(), *s.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(p, "verification.observed", VerificationObservation{start, result, FileObservation{Candidate: &current}}); err != nil {
		t.Fatal(err)
	}
	resumed, err := Verify(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != "READY" || len(resumed.Verification.Observations) != 2 {
		t.Fatal("incomplete resumed verification")
	}
	if !reflect.DeepEqual(resumed.Verification.Observations[0].Result, result) {
		t.Fatal("replaced completed check evidence")
	}
	if id, _ := resumed.Verification.Plan.ID(); id != planID {
		t.Fatal("resumption silently created another plan")
	}
}

func TestVerificationClosureRetainsUnknownAndRequiresExactAttestation(t *testing.T) {
	p, _, _, planID := verificationFixture(t)
	if err := Append(p, "verification.started", VerificationStart{planID, 0}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, actor, evidence string
		stopped             bool
	}{
		{"stale", "operator", "process tree inspected", true},
		{planID, "", "process tree inspected", true},
		{planID, "operator", "", true},
		{planID, "operator", "process tree inspected", false},
	} {
		if _, err := CloseVerification(context.Background(), p, tc.id, tc.actor, tc.evidence, tc.stopped); err == nil {
			t.Fatal("accepted incomplete attestation")
		}
	}
	s, err := CloseVerification(context.Background(), p, planID, "fixture-operator", "fixture launched no child; process quiescence checked", true)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "REPAIRING" || !s.Verification.Pending || s.Verification.Closure == nil || len(s.Verification.Observations) != 0 {
		t.Fatal("closure fabricated an outcome")
	}
	if _, err := CloseVerification(context.Background(), p, planID, "operator", "duplicate", true); err == nil {
		t.Fatal("duplicate closure")
	}
	s, err = Verify(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "READY" {
		t.Fatal("new explicitly requested verification failed")
	}
	if id, _ := s.Verification.Plan.ID(); id == planID {
		t.Fatal("reused abandoned attempt")
	}
}

func TestVerificationReadinessRequiresCompleteFreshEvidence(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ mode, state, status string }{
		{"child-pass", "READY", "PASS"}, {"child-fail", "REPAIRING", "FAIL"}, {"child-mutate", "REPAIRING", "PASS"}, {"missing", "REPAIRING", "NOT_RUN"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			argv := []string{exe, "-test.run=^TestVerificationControllerChild$", "--", tc.mode}
			if tc.mode == "missing" {
				argv = []string{"harness-deliberately-absent-tool-976253"}
			}
			p, _ := approvedRepository(t, config.Check{Name: "required", Argv: argv, TimeoutSeconds: 10})
			before, err := StartWorkspace(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			s, err := Verify(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			if s.State != tc.state || s.Verification.Pending || len(s.Verification.Observations) != 1 || s.Verification.Observations[0].Result.Status != tc.status {
				t.Fatalf("unexpected result: %+v", s)
			}
			if *s.Candidate != *before.Candidate {
				t.Fatal("verification silently admitted changed source")
			}
			if tc.mode == "child-mutate" {
				if _, err := Verify(context.Background(), p); err == nil {
					t.Fatal("reverified unapproved mutation")
				}
			}
		})
	}
}

func TestPendingVerificationCannotRerunOrDropChecks(t *testing.T) {
	p, s := writer(t)
	id, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := verification.PreparePlan(s.RunID, "explicit-attempt", id, s.Workspace.Request.Path, s.Creation.Config.Verification)
	if err != nil {
		t.Fatal(err)
	}
	bad := plan
	bad.Invocations = nil
	if err := Append(p, "verification.planned", bad); err == nil {
		t.Fatal("dropped required checks")
	}
	if err := Append(p, "verification.planned", plan); err != nil {
		t.Fatal(err)
	}
	planID, _ := plan.ID()
	if err := Append(p, "verification.started", VerificationStart{planID, 1}); err == nil {
		t.Fatal("skipped first check")
	}
	if err := Append(p, "verification.started", VerificationStart{planID, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), p); err == nil {
		t.Fatal("retried pending launch")
	}
	if err := Append(p, "verification.started", VerificationStart{planID, 0}); err == nil {
		t.Fatal("duplicated pending launch")
	}
}
