package control

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

func TestPromptRecipeCachePrefixV1PreservesMembersAndOrdersStableInstruction(t *testing.T) {
	type prompt struct {
		OutputSchema json.RawMessage `json:"output_schema,omitempty"`
		Instruction  string          `json:"instruction"`
		RunID        string          `json:"run_id"`
		CandidateID  string          `json:"candidate_id"`
		Objective    string          `json:"objective"`
	}
	policy := &ExecutionPolicy{PromptRecipe: promptRecipeCachePrefixV1}
	first, err := promptRecipeBytes(policy, prompt{OutputSchema: json.RawMessage(`{"type":"object"}`), Instruction: "stable role instruction", RunID: "run-a", CandidateID: strings.Repeat("a", 64), Objective: "change A"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := promptRecipeBytes(policy, prompt{OutputSchema: json.RawMessage(`{"type":"object"}`), Instruction: "stable role instruction", RunID: "run-b", CandidateID: strings.Repeat("b", 64), Objective: "change B"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(first), `{"instruction":"stable role instruction","candidate_id":`) {
		t.Fatalf("stable instruction is not the leading input prefix: %s", first)
	}
	if !strings.HasPrefix(string(second), `{"instruction":"stable role instruction","candidate_id":`) {
		t.Fatalf("second stable instruction is not the leading input prefix: %s", second)
	}
	firstPrefix := string(first[:strings.Index(string(first), `,"candidate_id"`)])
	secondPrefix := string(second[:strings.Index(string(second), `,"candidate_id"`)])
	if firstPrefix != secondPrefix {
		t.Fatal("cache-prefix-v1 changed the stable role instruction prefix")
	}
	legacy, err := canonical.Bytes(prompt{OutputSchema: json.RawMessage(`{"type":"object"}`), Instruction: "stable role instruction", RunID: "run-a", CandidateID: strings.Repeat("a", 64), Objective: "change A"})
	if err != nil {
		t.Fatal(err)
	}
	if normalized, err := canonical.Normalize(first); err != nil || string(normalized) != string(legacy) {
		t.Fatalf("recipe changed semantic JSON members: normalized=%s legacy=%s err=%v", normalized, legacy, err)
	}
	var decoded prompt
	if err := canonical.Decode(first, &decoded); err != nil || decoded.RunID != "run-a" || decoded.CandidateID != strings.Repeat("a", 64) || decoded.Objective != "change A" || string(decoded.OutputSchema) != `{"type":"object"}` {
		t.Fatalf("recipe omitted or changed prompt fields: %+v err=%v", decoded, err)
	}
}

func TestPromptRecipeEmptyPreservesCanonicalBytesAndRejectsUnknown(t *testing.T) {
	value := struct {
		Instruction string `json:"instruction"`
		CandidateID string `json:"candidate_id"`
	}{Instruction: "legacy", CandidateID: strings.Repeat("a", 64)}
	want, err := canonical.Bytes(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []*ExecutionPolicy{nil, {}, {PromptRecipe: ""}} {
		got, err := promptRecipeBytes(policy, value)
		if err != nil || string(got) != string(want) {
			t.Fatalf("empty recipe changed legacy bytes: got=%s want=%s err=%v", got, want, err)
		}
	}
	if _, err := promptRecipeBytes(&ExecutionPolicy{PromptRecipe: "unknown"}, value); err == nil {
		t.Fatal("unknown prompt recipe accepted")
	}
}

func TestPromptRecipeGuidanceIsVersionedAndRoleScoped(t *testing.T) {
	legacy := &ExecutionPolicy{}
	if got := promptRecipeInstruction(legacy, "reviewer", "json-v1", "Review."); got != "Review." {
		t.Fatalf("legacy reviewer instruction changed: %q", got)
	}
	policy := &ExecutionPolicy{PromptRecipe: promptRecipeCachePrefixV1}
	writer := promptRecipeInstruction(policy, "writer", "anchored-edits-v2", "Implement.")
	for _, required := range []string{"copy candidate_id, path, and before_hash verbatim", "exact same edits", "Do not recompose or guess"} {
		if !strings.Contains(writer, required) {
			t.Errorf("anchored writer guidance omitted %q", required)
		}
	}
	reviewer := promptRecipeInstruction(policy, "reviewer", "json-v1", "Review.")
	for _, required := range []string{"Preserve existing test expectations and contracts", "Changing tests merely to satisfy a broken implementation is not verification"} {
		if !strings.Contains(reviewer, required) {
			t.Errorf("reviewer guidance omitted %q", required)
		}
	}
	if got := promptRecipeInstruction(policy, "writer", "utf8-v2", "Implement."); got != "Implement." {
		t.Fatalf("anchored-only guidance leaked into a non-anchored writer: %q", got)
	}
	if got := promptRecipeInstruction(policy, "writer", "anchored-edits-v1", "Implement."); got != "Implement." {
		t.Fatalf("validation-tool guidance leaked into the v1 contract: %q", got)
	}
}
