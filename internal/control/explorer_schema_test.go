package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestExplorerSchemaOptInPreservesLegacyInvocationAndRejectsObjectSummary(t *testing.T) {
	c := creation(t)
	c.Config.Explorer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explorer", Effort: "low", Role: "explorer"}
	path, _ := approvedRepositoryCreation(t, c)
	s, err := StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := explorerInvocation(s, "Inspect source")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(legacy.Input, "output_schema") || strings.Contains(legacy.Input, "summary MUST") {
		t.Fatal("legacy invocation changed")
	}
	strict := s
	strict.Creation.Config.ExplorerContract = "json-v1"
	i, err := explorerInvocation(strict, "Inspect source")
	if err != nil || i.ID == legacy.ID || !strings.Contains(i.Input, "summary MUST") {
		t.Fatal("strict schema not bound into invocation", err)
	}
	var envelope struct {
		Schema json.RawMessage `json:"output_schema"`
	}
	if err := json.Unmarshal([]byte(i.Input), &envelope); err != nil || len(envelope.Schema) == 0 {
		t.Fatal("schema absent", err)
	}
	legacySchema, err := canonical.Normalize(runtime.ExplorerOutputSchema())
	if err != nil || string(envelope.Schema) != string(legacySchema) {
		t.Fatal("json-v1 schema recipe changed")
	}
	version2 := s
	version2.Creation.Config.ExplorerContract = "json-v2"
	v2, err := explorerInvocation(version2, "Inspect source")
	if err != nil || v2.ID == i.ID || !strings.Contains(v2.Input, "candidate_id MUST exactly match") {
		t.Fatal("candidate-bound schema not bound into json-v2 invocation", err)
	}
	var v2Envelope struct {
		Schema json.RawMessage `json:"output_schema"`
	}
	if err := json.Unmarshal([]byte(v2.Input), &v2Envelope); err != nil {
		t.Fatal(err)
	}
	var v2Schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(v2Envelope.Schema, &v2Schema); err != nil {
		t.Fatal(err)
	}
	if got := v2Schema.Properties["candidate_id"].Enum; len(got) != 1 || got[0] != idForSnapshot(t, s) {
		t.Fatal("json-v2 schema did not bind exact candidate", got)
	}
	id := idForSnapshot(t, s)
	model := legacy.Profile.Model
	r := ExplorerRecord{"Inspect source", legacy, runtime.Result{Version: 1, InvocationID: legacy.ID, Requested: legacy.Profile, ObservedModel: &model,
		Output: `{"candidate_id":"` + id + `","summary":{"observation":"source read"},"paths":[]}`}}
	if _, err := RecordExploration(path, r); err == nil {
		t.Fatal("object summary was silently admitted")
	}
	after, err := Inspect(path)
	if err != nil || len(after.Explorations) != 0 {
		t.Fatal("invalid result changed semantic history", err)
	}
	wrongID := strings.Repeat("0", 64)
	if wrongID == idForSnapshot(t, s) {
		wrongID = strings.Repeat("f", 64)
	}
	v2Model := v2.Profile.Model
	wrongCandidate := ExplorerRecord{"Inspect source", v2, runtime.Result{Version: 1, InvocationID: v2.ID, Requested: v2.Profile, ObservedModel: &v2Model,
		Output: `{"candidate_id":"` + wrongID + `","summary":"source read","paths":[]}`}}
	if _, err := RecordExploration(path, wrongCandidate); err == nil {
		t.Fatal("json-v2 result with a substituted candidate ID was admitted")
	}
}

func idForSnapshot(t *testing.T, s Snapshot) string {
	t.Helper()
	id, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
