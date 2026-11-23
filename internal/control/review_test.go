package control

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestConfiguredReviewGatesReadiness(t *testing.T) {
	for _, decision := range []string{"approve", "changes_requested"} {
		t.Run(decision, func(t *testing.T) {
			c := creation(t)
			c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-writer", Effort: "high", Role: "writer"}
			c.Config.Reviewer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-reviewer", Effort: "high", Role: "reviewer"}
			c.Config.Verification = []config.Check{{Name: "git-version", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}}
			path, _ := approvedRepositoryCreation(t, c)
			if _, err := StartWorkspace(context.Background(), path); err != nil {
				t.Fatal(err)
			}
			s, err := Verify(context.Background(), path)
			if err != nil || s.State != "REVIEWING" {
				t.Fatal("configured review bypassed", err)
			}
			i, err := PrepareReviewInvocation(path)
			if err != nil {
				t.Fatal(err)
			}
			id, err := s.Candidate.ID()
			if err != nil {
				t.Fatal(err)
			}
			findings := []ReviewFinding{}
			if decision == "changes_requested" {
				findings = append(findings, ReviewFinding{"file.txt", "Fixture finding: required behavior is absent."})
			}
			verdict := ReviewVerdict{CandidateID: id, VerificationPlanID: s.Verification.PlanID, Decision: decision, Findings: findings}
			makeRecord := func(v ReviewVerdict) ReviewRecord {
				output, err := canonical.Bytes(v)
				if err != nil {
					t.Fatal(err)
				}
				model := i.Profile.Model
				return ReviewRecord{i, runtime.Result{Version: 1, InvocationID: i.ID, Requested: i.Profile, ObservedModel: &model, Output: string(output)}}
			}
			bad := verdict
			bad.CandidateID = strings.Repeat("0", 64)
			if _, err := RecordReview(path, makeRecord(bad)); err == nil {
				t.Fatal("foreign review candidate admitted")
			}
			bad = verdict
			bad.VerificationPlanID = strings.Repeat("0", 64)
			if _, err := RecordReview(path, makeRecord(bad)); err == nil {
				t.Fatal("foreign verification admitted")
			}
			bad = verdict
			bad.Decision = "approve"
			bad.Findings = []ReviewFinding{{"file.txt", "unresolved issue"}}
			if _, err := RecordReview(path, makeRecord(bad)); err == nil {
				t.Fatal("contradictory approval admitted")
			}
			drift := filepath.Join(s.Workspace.Request.Path, "review-drift.txt")
			if err := os.WriteFile(drift, []byte("concurrent change"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := RecordReview(path, makeRecord(verdict)); err == nil {
				t.Fatal("review admitted a changed live candidate")
			}
			if err := os.Remove(drift); err != nil {
				t.Fatal(err)
			}
			s, err = RecordReview(path, makeRecord(verdict))
			if err != nil {
				t.Fatal(err)
			}
			want := "READY"
			if decision == "changes_requested" {
				want = "REPAIRING"
			}
			if s.State != want || s.Review == nil {
				t.Fatal("review transition mismatch")
			}
			if decision == "changes_requested" {
				writer, err := PrepareWriterInvocation(path)
				if err != nil {
					t.Fatal(err)
				}
				var input struct {
					Review writerReview `json:"review"`
				}
				if err := json.Unmarshal([]byte(writer.Input), &input); err != nil {
					t.Fatal(err)
				}
				if input.Review.InvocationID != i.ID || input.Review.CandidateID != id || input.Review.VerificationPlanID != verdict.VerificationPlanID || len(input.Review.Findings) != 1 || input.Review.Findings[0] != findings[0] || input.Review.Decision != decision {
					t.Fatal("review feedback not bound to repair input")
				}
				without := s
				without.Review = nil
				previous, err := writerInvocation(without)
				if err != nil {
					t.Fatal(err)
				}
				if previous.ID == writer.ID {
					t.Fatal("review evidence absent from writer identity")
				}
			}
			if _, err := RecordReview(path, makeRecord(verdict)); err == nil {
				t.Fatal("review replayed outside review phase")
			}
		})
	}
}

func TestReviewerJSONContractBindsOutputSchemaToInvocation(t *testing.T) {
	c := creation(t)
	c.Config.Reviewer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-reviewer", Effort: "high", Role: "reviewer"}
	c.Config.ReviewerContract = "json-v1"
	c.Config.Verification = []config.Check{{Name: "configured-unit", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}}
	path, _ := approvedRepositoryCreation(t, c)
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if s, err := Verify(context.Background(), path); err != nil || s.State != "REVIEWING" {
		t.Fatal("verification did not reach reviewer fixture", err)
	}
	i, err := PrepareReviewInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		OutputSchema json.RawMessage `json:"output_schema"`
		Instruction  string          `json:"instruction"`
	}
	if err := json.Unmarshal([]byte(i.Input), &payload); err != nil {
		t.Fatal(err)
	}
	wantSchema, err := canonical.Normalize(runtime.ReviewOutputSchema())
	if err != nil {
		t.Fatal(err)
	}
	gotSchema, err := canonical.Normalize(payload.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotSchema, wantSchema) || !strings.Contains(payload.Instruction, "plan recommendation is not evidence") || !strings.Contains(payload.Instruction, "cross-cutting concern") {
		t.Fatalf("reviewer JSON contract or verification scope was not invocation-bound: schema=%t instruction=%q", bytes.Equal(gotSchema, wantSchema), payload.Instruction)
	}
}
