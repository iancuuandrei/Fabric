package access

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/journal"
)

func TestRoutingDecisionEvidenceIsOptionalAndIdentityBound(t *testing.T) {
	_, _, intent := activeFixture(t)
	legacyID, err := intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(intent)
	if err != nil || bytes.Contains(raw, []byte("routing_decision")) {
		t.Fatalf("legacy intent emitted an empty routing evidence field: %s %v", raw, err)
	}
	intent.RoutingDecision = &RoutingDecision{
		Version: 1, ConfigID: strings.Repeat("c", 64), ProfileName: "standard",
		Model: intent.Route.Model, Effort: intent.Route.Effort,
		Reason: "default-configured-profile", ContextBytes: 32,
	}
	id, err := intent.ID()
	if err != nil || id == legacyID {
		t.Fatalf("opt-in evidence did not bind to invocation identity: %q %q %v", id, legacyID, err)
	}
	changed := intent
	copyEvidence := *intent.RoutingDecision
	changed.RoutingDecision = &copyEvidence
	copyEvidence.ContextBytes++
	changedID, err := changed.ID()
	if err != nil || changedID == id {
		t.Fatalf("mutated decision evidence did not change invocation identity: %q %q %v", changedID, id, err)
	}
	copyEvidence.Model = "substituted"
	if _, err := changed.ID(); err == nil {
		t.Fatal("evidence inconsistent with the admitted route was accepted")
	}
}

func TestCalibrationRoutingEvidenceV2IsIdentityBoundAndValidated(t *testing.T) {
	_, _, intent := activeFixture(t)
	intent.Route.Role = "fixer"
	intent.RoutingDecision = &RoutingDecision{
		Version: 2, ConfigID: strings.Repeat("c", 64), ProfileName: "sol",
		Model: intent.Route.Model, Effort: intent.Route.Effort,
		Reason: "calibrated-candidate", ContextBytes: 32, AcceptedFailures: 0,
		ObjectiveHash: strings.Repeat("d", 64),
		Calibration: &CalibrationDecision{
			Digest: strings.Repeat("e", 64), FamilyID: "go-fixer", BaselineProfile: "luna", CandidateProfile: "sol",
			SelectionReason: "candidate_quality_improved_train_and_holdout", ScopeMatched: true,
			CandidateSelected: true, CandidateApplied: true,
			Training: CalibrationArmCounts{
				Baseline:  CalibrationOutcomeCounts{Assigned: 1, Failed: 1},
				Candidate: CalibrationOutcomeCounts{Assigned: 1, Accepted: 1},
			},
			Holdout: CalibrationArmCounts{
				Baseline:  CalibrationOutcomeCounts{Assigned: 1, Failed: 1},
				Candidate: CalibrationOutcomeCounts{Assigned: 1, Accepted: 1},
			},
		},
	}
	id, err := intent.ID()
	if err != nil {
		t.Fatalf("valid v2 calibration evidence was rejected: %v", err)
	}
	changed := intent
	copyEvidence := *intent.RoutingDecision
	copyCalibration := *intent.RoutingDecision.Calibration
	copyEvidence.Calibration = &copyCalibration
	changed.RoutingDecision = &copyEvidence
	copyCalibration.Digest = strings.Repeat("f", 64)
	changedID, err := changed.ID()
	if err != nil || changedID == id {
		t.Fatalf("calibration artifact digest was not identity-bound: %q %q %v", id, changedID, err)
	}
	copyCalibration.Digest = intent.RoutingDecision.Calibration.Digest
	copyEvidence.ObjectiveHash = strings.Repeat("a", 64)
	changedID, err = changed.ID()
	if err != nil || changedID == id {
		t.Fatalf("objective digest was not identity-bound: %q %q %v", id, changedID, err)
	}
	copyEvidence.Reason = "calibration-objective-out-of-scope"
	if _, err := changed.ID(); err == nil {
		t.Fatal("out-of-scope evidence was allowed to claim the calibrated candidate")
	}
}

func TestDurableAdmissionReplaysBudgetAndPolicy(t *testing.T) {
	profile := fixture()
	profile.Kind, profile.CredentialRef, profile.AuthMode = "subscription", "", "session"
	pid, _ := profile.ID()
	route := Route{Version: 1, Role: "planner", Runtime: profile.Runtime, Provider: profile.Provider, Model: "fixture", Effort: "none", AccessID: pid, Permission: "read-only"}
	policy := Policy{Version: 1, RunID: strings.Repeat("a", 64), Class: Public, Limits: Limits{Tokens: 100, Concurrency: 1}, Routes: []Route{route}, Profiles: []Profile{profile}}
	id, err := policy.ID()
	if err != nil {
		t.Fatal(err)
	}
	i := Intent{Attempt: 1, PolicyID: id, InputHash: strings.Repeat("b", 64), Route: route, Reservation: Reservation{Tokens: 50, BillingMode: "subscription"}}
	i.Reservation.InvocationID, err = i.ID()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "access.jsonl")
	if RequireActive(path, policy, i) == nil {
		t.Fatal("missing reservation accepted")
	}
	for _, mutate := range []func(*Intent){
		func(i *Intent) { i.InputHash = strings.Repeat("e", 64) },
		func(i *Intent) { i.Attempt++ },
		func(i *Intent) { i.Reservation.Tokens-- },
		func(i *Intent) { i.Reservation.InvocationID = strings.Repeat("f", 64) },
		func(i *Intent) { i.Route.Model = "substituted" },
	} {
		changed := i
		mutate(&changed)
		if ReserveDurable(path, policy, changed) == nil {
			t.Fatal("mutated invocation persisted")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("rejected invocation created journal", err)
		}
	}
	if err := ReserveDurable(path, policy, i); err != nil {
		t.Fatal(err)
	}
	admitted := i
	if err := RequireActive(path, policy, admitted); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	i.Attempt++
	i.Reservation.InvocationID, _ = i.ID()
	if ReserveDurable(path, policy, i) == nil {
		t.Fatal("restart forgot pending budget")
	}
	policy.Limits.Concurrency = 2
	i.PolicyID, _ = policy.ID()
	i.Reservation.InvocationID, _ = i.ID()
	if ReserveDurable(path, policy, i) == nil {
		t.Fatal("policy widened on replay")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("denied admission changed journal")
	}
	if events, err := journal.Read(path); err != nil || len(events) != 1 {
		t.Fatal("durable intent missing", err)
	}
	policy.Limits.Concurrency = 1
	if RequireActive(path, policy, i) == nil {
		t.Fatal("different intent accepted")
	}
	routeID, _ := route.ID()
	if err := RecordTerminal(path, policy, Receipt{InvocationID: admitted.Reservation.InvocationID, RouteID: routeID, Status: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	if RequireActive(path, policy, admitted) == nil {
		t.Fatal("terminal reservation accepted")
	}
}
