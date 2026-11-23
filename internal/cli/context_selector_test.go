package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/control"
)

func TestAutonomousContextSelectorRejectsUnknownPreRun(t *testing.T) {
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	err := Execute(context.Background(), []string{"run", "--autonomous", "--context-selector", "unknown-mode", "Make a bounded fixture change"}, root, &out)
	if err == nil || !strings.Contains(err.Error(), "context-selector") {
		t.Fatalf("unknown selector not rejected pre-run: %v", err)
	}
	if entries, gerr := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl")); gerr != nil || len(entries) != 0 {
		t.Fatalf("malformed selector created a run: %v, %v", entries, gerr)
	}
}

func TestAutonomousContextSelectorAbsencePreservesLegacy(t *testing.T) {
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	_ = Execute(context.Background(), []string{"run", "--autonomous", "Make a bounded fixture change"}, root, &out)
	entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one durable run: %v, %v", entries, err)
	}
	s, err := control.Inspect(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if s.Creation.Execution == nil || s.Creation.Execution.ContextSelector != "" {
		t.Fatalf("absence did not preserve legacy selector: %#v", s.Creation.Execution)
	}
}

func TestAutonomousContextSelectorAdmitsRRF(t *testing.T) {
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	_ = Execute(context.Background(), []string{"run", "--autonomous", "--context-selector", "rrf-coverage-v1", "Make a bounded fixture change"}, root, &out)
	entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one durable run: %v, %v", entries, err)
	}
	s, err := control.Inspect(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	policy := s.Creation.Execution
	if policy == nil || policy.ContextSelector != "rrf-coverage-v1" || policy.Context != "bounded-v1" {
		t.Fatalf("rrf-coverage-v1 not bound to immutable policy: %#v", policy)
	}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestInspectPlanReportsContextSelector(t *testing.T) {
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	if err := Execute(context.Background(), []string{"run", "--autonomous", "--inspect-plan", "--context-selector", "rrf-coverage-v1"}, root, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "rrf-coverage-v1") {
		t.Fatalf("inspect-plan omits context selector: %s", out.String())
	}
}
