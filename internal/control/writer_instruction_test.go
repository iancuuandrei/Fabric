package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"harness.local/engorch/internal/runtime"
)

// utf8WriterInvocationFixture prepares one fake utf8-v2 writer invocation on
// a fresh approved workspace. Shared by the writer instruction, transport and
// journal tests so the explicit-writer fixture construction stays exact in
// one place.
func utf8WriterInvocationFixture(t *testing.T) (string, runtime.Invocation) {
	t.Helper()
	c := creation(t)
	c.Config.WriterContract = "utf8-v2"
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
