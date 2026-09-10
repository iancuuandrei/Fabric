package control

import (
	"encoding/json"
	"strings"
	"testing"

	"harness.local/engorch/internal/writercontract"
)

func changesJSONOuter(t *testing.T, candidate string, inner string) string {
	t.Helper()
	rawInner, err := json.Marshal(inner)
	if err != nil {
		t.Fatal(err)
	}
	return `{"candidate_id":` + `"` + candidate + `","changes_json":` + string(rawInner) + `}`
}

func TestChangesJSONContractAcceptsValid(t *testing.T) {
	id := strings.Repeat("a", 64)
	inner := `[{"path":"internal/example.go","before_hash":null,"content_utf8":"package example\n","executable":false}]`
	p, err := decodeWriterProposal(writercontract.ContractChangesJSONV1, changesJSONOuter(t, id, inner))
	if err != nil {
		t.Fatal(err)
	}
	if p.CandidateID != id || len(p.Changes) != 1 || p.Changes[0].Path != "internal/example.go" {
		t.Fatal("valid v2 proposal not preserved")
	}
	if p.Changes[0].ContentBase64 == nil {
		t.Fatal("utf8 content not converted deterministically")
	}
}

func TestChangesJSONContractRejectsMatrix(t *testing.T) {
	id := strings.Repeat("b", 64)
	cases := map[string]string{
		"malformed":             changesJSONOuter(t, id, `[{"path":`),
		"object instead array":  changesJSONOuter(t, id, `{"path":"x"}`),
		"double-stringified":    changesJSONOuter(t, id, `"[{\\\"path\\\":\\\"x\\\"}]"`),
		"null":                  changesJSONOuter(t, id, `null`),
		"empty array":           changesJSONOuter(t, id, `[]`),
		"unknown change field":  changesJSONOuter(t, id, `[{"path":"x","before_hash":null,"content_utf8":"a","executable":false,"extra":1}]`),
		"missing content":       changesJSONOuter(t, id, `[{"path":"x","before_hash":null,"executable":false}]`),
		"content wrong type":    changesJSONOuter(t, id, `[{"path":"x","before_hash":null,"content_utf8":123,"executable":false}]`),
		"executable wrong type": changesJSONOuter(t, id, `[{"path":"x","before_hash":null,"content_utf8":"a","executable":"false"}]`),
		"inner envelope":        changesJSONOuter(t, id, `{"candidate_id":"`+id+`","changes":[]}`),
		"missing changes_json":  `{"candidate_id":"` + id + `"}`,
		"extra outer field":     `{"candidate_id":"` + id + `","changes_json":"[]","changes":[]}`,
		"old v1 shape under v2": `{"candidate_id":"` + id + `","changes":[{"path":"x","before_hash":null,"content_utf8":"a","executable":false}]}`,
		"changes_json not str":  `{"candidate_id":"` + id + `","changes_json":[]}`,
		"duplicate outer key":   `{"candidate_id":"` + id + `","candidate_id":"` + id + `","changes_json":"[]"}`,
		"duplicate inner key":   changesJSONOuter(t, id, `[{"path":"x","path":"y","before_hash":null,"content_utf8":"a","executable":false}]`),
		"non-object array item": changesJSONOuter(t, id, `["x"]`),
		"content deletion null": ``, // placeholder replaced below
	}
	// deletion (content_utf8 null) is valid wire; drop placeholder
	delete(cases, "content deletion null")
	// >64 changes
	big := `[` + strings.Repeat(`{"path":"x","before_hash":null,"content_utf8":"a","executable":false},`, 64) + `{"path":"y","before_hash":null,"content_utf8":"a","executable":false}]`
	cases[">64 changes"] = changesJSONOuter(t, id, big)
	for name, raw := range cases {
		if _, err := decodeWriterProposal(writercontract.ContractChangesJSONV1, raw); err == nil {
			t.Fatalf("v2 accepted %s", name)
		}
	}
	// deletion null stays valid (explicit null means delete)
	delRaw := changesJSONOuter(t, id, `[{"path":"x","before_hash":null,"content_utf8":null,"executable":false}]`)
	if _, err := decodeWriterProposal(writercontract.ContractChangesJSONV1, delRaw); err != nil {
		t.Fatalf("v2 rejected explicit deletion null: %v", err)
	}
}

func TestChangesJSONContractHasNoDualAccept(t *testing.T) {
	id := strings.Repeat("c", 64)
	// v1 shape (nested array) must stay invalid under v2 contract
	v1 := `{"candidate_id":"` + id + `","changes":[{"path":"x","before_hash":null,"content_utf8":"a","executable":false}]}`
	if _, err := decodeWriterProposal(writercontract.ContractChangesJSONV1, v1); err == nil {
		t.Fatal("v2 accepted v1 nested array shape")
	}
	// v2 stringified shape must stay invalid under v1 contract
	inner := `[{"path":"x","before_hash":null,"content_utf8":"a","executable":false}]`
	rawInner, _ := json.Marshal(inner)
	v2underV1 := `{"candidate_id":"` + id + `","changes":` + string(rawInner) + `}`
	if _, err := decodeWriterProposal("utf8-v2", v2underV1); err == nil {
		t.Fatal("v1 accepted stringified changes")
	}
}

func TestChangesJSONSchemaShape(t *testing.T) {
	schema := string(writercontract.ChangesJSONSchema())
	for _, want := range []string{`"candidate_id"`, `"changes_json"`, `"additionalProperties":false`} {
		if !strings.Contains(schema, want) {
			t.Fatalf("v2 schema missing %s", want)
		}
	}
	if strings.Contains(schema, `"changes"`) && !strings.Contains(schema, `"changes_json"`) {
		t.Fatal("v2 schema kept nested changes array")
	}
}
