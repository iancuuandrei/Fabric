package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestSemanticCorrectionPreservesNativeOutputSchema(t *testing.T) {
	profile := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "fixture", Effort: "high", Role: "planner"}
	input, err := canonical.Bytes(struct {
		OutputSchema json.RawMessage `json:"output_schema"`
	}{engineeringplan.PlannerJSONSchema()})
	if err != nil {
		t.Fatal(err)
	}
	original, err := runtime.NewInvocationWithCodexAutoCompact(profile, string(input), nil)
	if err != nil {
		t.Fatal(err)
	}
	model := profile.Model
	result := runtime.Result{Version: 1, InvocationID: original.ID, Requested: profile, ObservedModel: &model, Output: "{}"}
	corrected, err := buildSemanticCorrectionInvocation(original, result, strings.Repeat("a", 64), 1, 2, "missing graph")
	if err != nil {
		t.Fatal(err)
	}
	var envelope semanticCorrectionInput
	if err := json.Unmarshal([]byte(corrected.Input), &envelope); err != nil {
		t.Fatal(err)
	}
	want, _ := canonical.Hash("schema", engineeringplan.PlannerJSONSchema())
	got, _ := canonical.Hash("schema", envelope.OutputSchema)
	if got != want || envelope.Rejection != "missing graph" {
		t.Fatal("native contract or rejection changed")
	}
}

func TestSemanticOutputFailureRejectsJoinedStrictErrors(t *testing.T) {
	content := rejectedSemanticOutput(errors.New("invalid output contract"))
	if !errors.Is(content, ErrSemanticOutputInvalid) || !semanticOutputFailureOnly(fmt.Errorf("proposal: %w", content)) {
		t.Fatal("typed content rejection lost classification")
	}
	for _, strict := range []error{ErrAutonomousUnsafe, ErrAutonomousReconciliation, errors.New("lease close failed")} {
		if semanticOutputFailureOnly(errors.Join(content, strict)) {
			t.Fatal("strict joined failure authorized semantic correction")
		}
	}
	if semanticOutputFailureOnly(ErrSemanticOutputInvalid) || semanticOutputFailureOnly(nil) {
		t.Fatal("unbound sentinel authorized correction")
	}
}

func TestPlannerSemanticCorrectionReplayAndBound(t *testing.T) {
	_, initial := graphAwaitingApproval(t, graphCreation(t, 1))
	c := initial.Creation
	c.Execution.SemanticCorrectionVersion = 1
	path := filepath.Join(t.TempDir(), "correction.db")
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		s, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		invocation, err := plannerInvocationForSnapshot(s)
		if err != nil {
			t.Fatal(err)
		}
		model := invocation.Profile.Model
		result := runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, Output: "{}"}
		if err := Append(path, "plan.recorded", result); err != nil {
			t.Fatal(err)
		}
		s, err = Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		priorHead := s.ControllerHead
		uncertain := s
		uncertain.FileOutcome = "UNKNOWN"
		if _, err := expectedPlannerSemanticCorrection(uncertain); !errors.Is(err, ErrAutonomousReconciliation) {
			t.Fatalf("uncertainty authorized correction: %v", err)
		}
		err = startPlannerSemanticCorrection(path, s)
		if attempt == 2 {
			if !errors.Is(err, ErrSemanticCorrectionBudget) {
				t.Fatalf("third correction admitted: %v", err)
			}
			after, inspectErr := Inspect(path)
			if inspectErr != nil || after.ControllerHead != priorHead || after.RepairAttempts != 0 {
				t.Fatal("exhaustion changed durable state or repair budget", inspectErr)
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		after, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		next, err := plannerInvocationForSnapshot(after)
		if err != nil || next.ID == invocation.ID || after.State != "PLANNING" || after.Plan != nil || len(after.PlannerCorrections) != attempt+1 || after.RepairAttempts != 0 {
			t.Fatalf("restart failed correction identity/budget: %+v %v", after, err)
		}
		if after.PlannerCorrections[attempt].Predecessor.Output != result.Output {
			t.Fatal("rejected output was not retained")
		}
		if after.PlannerCorrections[attempt].Rejection == "" || !strings.Contains(next.Input, `"rejection":`) {
			t.Fatal("exact validation diagnostic missing from correction evidence/input")
		}
		var before, corrected struct {
			OutputSchema json.RawMessage `json:"output_schema"`
		}
		if err := json.Unmarshal([]byte(invocation.Input), &before); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(next.Input), &corrected); err != nil {
			t.Fatal(err)
		}
		beforeHash, _ := canonical.Hash("schema", before.OutputSchema)
		afterHash, _ := canonical.Hash("schema", corrected.OutputSchema)
		if beforeHash != afterHash {
			t.Fatal("native planner schema lost during correction")
		}
	}
}

func TestSemanticCorrectionHasNewIdentityAndPreservesRoute(t *testing.T) {
	for _, role := range []string{"planner", "writer", "fixer", "reviewer"} {
		t.Run(role, func(t *testing.T) {
			profile := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "fixture", Effort: "high", Role: role}
			original, err := runtime.NewInvocationWithCodexAutoCompact(profile, "original contract", &runtime.CodexAutoCompactOptions{Version: 1, TokenLimit: 32000})
			if err != nil {
				t.Fatal(err)
			}
			model, provider, effort := profile.Model, profile.Provider, profile.Effort
			result := runtime.Result{Version: 1, InvocationID: original.ID, Requested: profile, ObservedModel: &model, ObservedProvider: &provider, ObservedEffort: &effort, Output: "malformed output"}
			first, err := buildSemanticCorrectionInvocation(original, result, strings.Repeat("a", 64), 1, 2)
			if err != nil {
				t.Fatal(err)
			}
			second, err := buildSemanticCorrectionInvocation(original, result, strings.Repeat("a", 64), 2, 2)
			if err != nil {
				t.Fatal(err)
			}
			if first.ID == original.ID || second.ID == first.ID || first.Profile != profile || first.CodexAutoCompactTokenLimit != original.CodexAutoCompactTokenLimit {
				t.Fatal("correction resent original identity or changed its route")
			}
			if _, err := buildSemanticCorrectionInvocation(original, result, strings.Repeat("a", 64), 3, 2); !errors.Is(err, ErrSemanticCorrectionBudget) {
				t.Fatal("correction exceeded separate allowance", err)
			}
			if _, err := buildSemanticCorrectionInvocation(original, result, "missing", 1, 2); !errors.Is(err, ErrAutonomousUnsafe) {
				t.Fatal("correction accepted missing receipt head", err)
			}
			result.InvocationID = strings.Repeat("b", 64)
			if _, err := buildSemanticCorrectionInvocation(original, result, strings.Repeat("a", 64), 1, 2); !errors.Is(err, ErrAutonomousUnsafe) {
				t.Fatal("correction accepted substituted predecessor", err)
			}
		})
	}
}
