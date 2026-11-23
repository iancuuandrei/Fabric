package control

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestReviewerSemanticCorrectionPreservesVerificationAndRejectsUnsafeBinding(t *testing.T) {
	c := creation(t)
	c.Config.Reviewer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "review-fixture", Effort: "high", Role: "reviewer"}
	c.Config.Verification = []config.Check{{Name: "git-version", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}}
	path, _ := approvedRepositoryCreation(t, c)
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	s, err := Verify(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	s.Creation.Execution = &ExecutionPolicy{GraphVersion: 1, SemanticCorrectionVersion: 1}
	s.Creation.Config.Reviewer.Runtime = "codex-app-server"
	s.Creation.Config.Reviewer.Provider = "openai"
	invocation, err := reviewInvocation(s)
	if err != nil {
		t.Fatal(err)
	}
	model := invocation.Profile.Model
	result := runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, Output: "not JSON"}
	bind := func(result runtime.Result) {
		hash, err := canonical.Hash("harness.review-result.v1", result)
		if err != nil {
			t.Fatal(err)
		}
		s.ReviewHost = &ReviewHostState{Intent: ReviewHostIntent{Invocation: invocation}, RuntimeReceipt: &ReviewRuntimeReceipt{InvocationID: invocation.ID, ResultHash: hash, JournalHead: strings.Repeat("a", 64)}}
	}
	bind(result)
	correction, err := expectedRoleSemanticCorrection(s, invocation, result)
	if err != nil {
		t.Fatal(err)
	}
	verification := s.Verification
	payload, err := canonical.Bytes(correction)
	if err != nil {
		t.Fatal(err)
	}
	if err := replayRoleSemanticCorrection(&s, journal.Event{Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if s.State != "REVIEWING" || s.Verification != verification || s.Review != nil {
		t.Fatal("correction changed verification or accepted a verdict")
	}
	next, err := reviewInvocation(s)
	if err != nil || next != correction.Invocation {
		t.Fatal("review correction identity missing", err)
	}
	// Bind a completed but wrong-candidate verdict to its exact new receipt.
	invocation = next
	bad, err := canonical.Bytes(ReviewVerdict{CandidateID: strings.Repeat("b", 64), VerificationPlanID: verification.PlanID, Decision: "approve", Findings: []ReviewFinding{}})
	if err != nil {
		t.Fatal(err)
	}
	result.InvocationID = next.ID
	result.Output = string(bad)
	bind(result)
	if _, err := expectedRoleSemanticCorrection(s, next, result); err == nil {
		t.Fatal("wrong-candidate verdict admitted correction")
	}
}

// This is a local admission/replay boundary fixture, not provider qualification.
func TestWriterSemanticCorrectionRequiresExactCompletedReceipt(t *testing.T) {
	c := creation(t)
	c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "writer-fixture", Effort: "high", Role: "writer"}
	path, _ := approvedRepositoryCreation(t, c)
	s, err := StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	s.Creation.Execution = &ExecutionPolicy{GraphVersion: 1, SemanticCorrectionVersion: 1}
	s.Creation.Config.Writer.Runtime = "codex-app-server"
	s.Creation.Config.Writer.Provider = "openai"
	var immutableBaseID string
	for attempt := 1; attempt <= 3; attempt++ {
		invocation, err := writerInvocation(s)
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 1 {
			immutableBaseID = invocation.ID
		}
		model := invocation.Profile.Model
		result := runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, Output: "not JSON"}
		hash, err := canonical.Hash("harness.writer-result.v1", result)
		if err != nil {
			t.Fatal(err)
		}
		s.WriterHost = &WriterHostState{Intent: WriterHostIntent{Invocation: invocation}, RuntimeReceipt: &WriterRuntimeReceipt{InvocationID: invocation.ID, ResultHash: hash, JournalHead: strings.Repeat("a", 64)}}
		correction, err := expectedRoleSemanticCorrection(s, invocation, result)
		if attempt == 3 {
			if !errors.Is(err, ErrSemanticCorrectionBudget) {
				t.Fatal("separate correction allowance not enforced", err)
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if correction.Attempt != attempt || correction.Invocation.ID == invocation.ID || correction.Rejection == "" || correction.Predecessor.Output != result.Output {
			t.Fatal("correction evidence incomplete")
		}
		if correction.BaseInvocationID != immutableBaseID {
			t.Fatal("successor reset the immutable correction chain and its allowance")
		}
		uncertain := s
		uncertain.FileOutcome = "UNKNOWN"
		if _, err := expectedRoleSemanticCorrection(uncertain, invocation, result); !errors.Is(err, ErrAutonomousReconciliation) {
			t.Fatal("uncertain effect admitted correction", err)
		}
		missing := s
		missing.WriterHost = nil
		if _, err := expectedRoleSemanticCorrection(missing, invocation, result); err == nil {
			t.Fatal("missing receipt admitted correction")
		}
		payload, err := canonical.Bytes(correction)
		if err != nil {
			t.Fatal(err)
		}
		before := s
		for _, mutate := range []func(*RoleSemanticCorrection){
			func(c *RoleSemanticCorrection) { c.CandidateID = strings.Repeat("b", 64) },
			func(c *RoleSemanticCorrection) { c.Rejection = "substituted diagnostic" },
			func(c *RoleSemanticCorrection) { c.ReceiptHead = strings.Repeat("b", 64) },
			func(c *RoleSemanticCorrection) { c.Attempt++ },
		} {
			bad := correction
			mutate(&bad)
			badPayload, err := canonical.Bytes(bad)
			if err != nil {
				t.Fatal(err)
			}
			copy := before
			if err := replayRoleSemanticCorrection(&copy, journal.Event{Payload: badPayload}); !errors.Is(err, ErrAutonomousUnsafe) {
				t.Fatal("substituted correction admitted", err)
			}
		}
		if err := replayRoleSemanticCorrection(&s, journal.Event{Payload: payload}); err != nil {
			t.Fatal(err)
		}
		next, err := writerInvocation(s)
		if err != nil || next != correction.Invocation {
			t.Fatal("replayed correction did not select its new invocation", err)
		}
		if err := replayRoleSemanticCorrection(&s, journal.Event{Payload: payload}); err == nil {
			t.Fatal("duplicate correction replay admitted")
		}
	}
}
