package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

// utf8WriterInvocationFixture prepares one fake utf8-v2 writer invocation on
// a fresh approved workspace. Shared by the writer instruction, transport and
// journal tests so the explicit-writer fixture construction stays exact in
// one place.
func utf8WriterInvocationFixture(t *testing.T) (string, runtime.Invocation) {
	return writerInvocationFixtureForContract(t, "utf8-v2")
}

func writerInvocationFixtureForContract(t *testing.T, contract string) (string, runtime.Invocation) {
	t.Helper()
	c := creation(t)
	c.Config.WriterContract = contract
	c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-writer", Effort: "high", Role: "writer"}
	path, _ := approvedRepositoryCreation(t, c)
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	invocation, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, invocation
}

func TestUTF8ReplaceV3RequiresCompleteFileReadsAndPreservation(t *testing.T) {
	_, invocation := writerInvocationFixtureForContract(t, "utf8-replace-v3")
	var input struct {
		Instruction string          `json:"instruction"`
		Schema      json.RawMessage `json:"output_schema"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &input); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"complete-file replacement",
		"candidate_read",
		"entire current candidate file",
		"source_read",
		"complete committed base file",
		"preserve unrelated code",
		"Do not use patch fragments as file contents",
	} {
		if !strings.Contains(input.Instruction, required) {
			t.Fatalf("v3 writer instruction omitted %q", required)
		}
	}
	if strings.Contains(string(input.Schema), "content_base64") || !strings.Contains(string(input.Schema), "content_utf8") {
		t.Fatal("v3 must reuse the strict UTF-8 output schema")
	}
}

func TestUTF8ScopedV4UsesCurrentReadyRepairTaskAndCandidateSchema(t *testing.T) {
	path, _ := writerInvocationFixtureForContract(t, "utf8-scoped-v4")
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	g := validGraphFixture()
	for i := range g.Tasks {
		switch g.Tasks[i].Kind {
		case engineeringplan.Research, engineeringplan.Design, engineeringplan.Implementation, engineeringplan.Verification:
			g.Tasks[i].Completed = true
			g.Tasks[i].Attempts = []engineeringplan.Attempt{{ID: "attempt-1", Outcome: engineeringplan.AttemptCompleted}}
		case engineeringplan.Review:
			g.Tasks[i].Attempts = []engineeringplan.Attempt{{ID: "attempt-1", Outcome: engineeringplan.AttemptFailed}}
		}
	}
	failed := g.Tasks[len(g.Tasks)-1]
	g, err = engineeringplan.RepairExtension(g, failed.ID, "attempt-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	s.Graph = &GraphState{Graph: g, Evidence: map[string]GraphTaskEvidence{
		failed.ID: {TaskID: failed.ID, AttemptID: "attempt-1", Outcome: "failed", ReviewInvocationID: "attempt-1"},
	}}
	invocation, err := writerInvocation(s)
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		CandidateID string          `json:"candidate_id"`
		Instruction string          `json:"instruction"`
		Schema      json.RawMessage `json:"output_schema"`
		Task        struct {
			ID               string                     `json:"id"`
			ScopePaths       []string                   `json:"scope_paths"`
			WritePaths       []string                   `json:"write_paths"`
			ExpectedEvidence []engineeringplan.Evidence `json:"expected_evidence"`
		} `json:"implementation_task"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &input); err != nil {
		t.Fatal(err)
	}
	if input.Task.ID != "impl-repair-1" || len(input.Task.WritePaths) != 1 || input.Task.WritePaths[0] != "file.txt" || len(input.Task.ScopePaths) != 1 || input.Task.ScopePaths[0] != "file.txt" || len(input.Task.ExpectedEvidence) != 3 {
		t.Fatalf("v4 did not bind the current repair task: %+v", input.Task)
	}
	if !strings.Contains(input.Instruction, "only under its exact write_paths") || !strings.Contains(input.Instruction, "Do not write outside write_paths or widen the task") {
		t.Fatal("v4 instruction omitted graph write boundary")
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(input.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if got := schema.Properties["candidate_id"].Enum; len(got) != 1 || got[0] != input.CandidateID {
		t.Fatalf("v4 schema candidate enum mismatch: %v candidate=%q", got, input.CandidateID)
	}
	legacyPath, _ := writerInvocationFixtureForContract(t, "utf8-replace-v3")
	legacySnapshot, err := Inspect(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	legacySnapshot.Graph = &GraphState{Graph: directGraphFixture()}
	legacy, err := writerInvocation(legacySnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(legacy.Input, "implementation_task") || strings.Contains(legacy.Input, "single currently ready graph task") || !strings.Contains(legacy.Input, `"pattern":"^[0-9a-f]{64}$"`) {
		t.Fatal("v3 invocation recipe changed")
	}
}

func TestUTF8ScopedV4RejectsAmbiguousGraphImplementations(t *testing.T) {
	path, _ := writerInvocationFixtureForContract(t, "utf8-scoped-v4")
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	g := validGraphFixture()
	for i := range g.Tasks {
		if g.Tasks[i].Kind == engineeringplan.Research || g.Tasks[i].Kind == engineeringplan.Design {
			g.Tasks[i].Completed = true
			g.Tasks[i].Attempts = []engineeringplan.Attempt{{ID: "attempt-1", Outcome: engineeringplan.AttemptCompleted}}
		}
	}
	second := engineeringplan.Task{ID: "impl-two", Kind: engineeringplan.Implementation, Title: "Second implementation", Dependencies: []string{"research-a", "research-b"}, ScopePaths: []string{"other.txt"}, WritePaths: []string{"other.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "second output"}}, EstimatedSeconds: 10}
	g.Tasks = append(g.Tasks, second)
	s.Graph = &GraphState{Graph: g}
	if _, err := writerInvocation(s); err == nil || !strings.Contains(err.Error(), "exactly one ready graph implementation") {
		t.Fatalf("ambiguous graph writer task was not rejected: %v", err)
	}
}

func TestUTF8ScopedV4RejectsUnrecordedEnabledGraph(t *testing.T) {
	path, _ := writerInvocationFixtureForContract(t, "utf8-scoped-v4")
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Creation.Execution = &ExecutionPolicy{Mode: "autonomous-v1", GraphVersion: 1}
	if _, err := writerInvocation(s); err == nil || !strings.Contains(err.Error(), "graph to be recorded") {
		t.Fatalf("unrecorded graph allowed writer dispatch: %v", err)
	}
}

func TestUTF8ScopedV4NonGraphPlanDoesNotFabricateGraphScope(t *testing.T) {
	_, invocation := writerInvocationFixtureForContract(t, "utf8-scoped-v4")
	var input struct {
		Instruction        string          `json:"instruction"`
		ImplementationTask json.RawMessage `json:"implementation_task"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &input); err != nil {
		t.Fatal(err)
	}
	if len(input.ImplementationTask) != 0 || !strings.Contains(input.Instruction, "No graph implementation_task is present") {
		t.Fatal("ordinary plan acquired synthetic graph scope")
	}
}

// The M2ak/M2am writers produced valid proposals with one fatal nesting
// error: the changes array stringified into a JSON string. The strict
// decoder already rejects that shape; the utf8-v2 instruction must
// additionally state the array requirement with a concrete minimal example
// so the model does not repeat it.
func TestUTF8WriterInstructionRequiresChangesArray(t *testing.T) {
	_, invocation := utf8WriterInvocationFixture(t)
	var input struct {
		Instruction string `json:"instruction"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &input); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(input.Instruction, "MUST be a JSON array of change objects, never a JSON string") {
		t.Fatal("utf8-v2 writer instruction lost the changes-array requirement")
	}
	if !strings.Contains(input.Instruction, `"changes":[{"path":"internal/example.go"`) {
		t.Fatal("utf8-v2 writer instruction lost the exact-shape example")
	}
}

func TestNestedStringChangesReplyRejected(t *testing.T) {
	path, invocation := utf8WriterInvocationFixture(t)
	s, err := Inspect(path)
	if err != nil || s.Candidate == nil {
		t.Fatal("candidate unavailable", err)
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	// Exact M2ak failure shape, minimized: changes stringified.
	nested := `{"candidate_id":"` + candidateID + `","changes":"[{\\"path\\":\\"file.txt\\"}]"}`
	result := runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, Output: nested}
	if _, err := PrepareWriterFiles(context.Background(), path, invocation, result); err == nil {
		t.Fatal("stringified changes array admitted")
	}
}
