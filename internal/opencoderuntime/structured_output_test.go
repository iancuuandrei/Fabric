package opencoderuntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/opencode"
	"github.com/iancuuandrei/fabric/internal/providergateway"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

func TestStructuredOutputIntentBindsExactWriterSchemaAndRole(t *testing.T) {
	schema := writercontract.UTF8Schema()
	expectation, err := opencode.NewStructuredOutputExpectation(schema)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "engorch-openai", Model: "writer-model", Effort: "none", Role: "writer"}, `{"output_schema":`+string(schema)+`,"instruction":"write"}`)
	if err != nil {
		t.Fatal(err)
	}
	intent := Intent{Version: 2, Invocation: invocation, StructuredOutput: &expectation}
	if err := validateStructuredOutputIntent(intent); err != nil {
		t.Fatal("exact utf8-v2 schema was rejected", err)
	}

	mutated := invocation
	mutated.Input = strings.Replace(mutated.Input, `"candidate_id"`, `"unauthorized"`, 1)
	intent.Invocation = mutated
	if err := validateStructuredOutputIntent(intent); err == nil {
		t.Fatal("unauthorized output_schema mutation was admitted")
	}

	intent.Invocation = invocation
	intent.Invocation.Profile.Role = "planner"
	if err := validateStructuredOutputIntent(intent); err == nil {
		t.Fatal("read-only planner acquired native writer output")
	}
	intent.Invocation.Profile.Role = "writer"
	intent.Version = 3
	if err := validateStructuredOutputIntent(intent); err == nil {
		t.Fatal("composite native writer output was admitted")
	}
}

func TestStructuredOutputIntentBindsCandidateSpecificV4Schema(t *testing.T) {
	candidate := strings.Repeat("a", 64)
	schema, err := writercontract.UTF8SchemaForCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	profile := runtime.Profile{Runtime: "opencode-http", Provider: "engorch-openai", Model: "writer-model", Effort: "none", Role: "writer"}
	input, err := json.Marshal(map[string]any{"candidate_id": candidate, "output_schema": schema, "instruction": "write"})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(profile, string(input))
	if err != nil {
		t.Fatal(err)
	}
	expectation, err := opencode.NewStructuredOutputExpectation(schema)
	if err != nil {
		t.Fatal(err)
	}
	intent := Intent{Version: 2, Invocation: invocation, StructuredOutput: &expectation}
	if err := validateStructuredOutputIntent(intent); err != nil {
		t.Fatal("exact candidate-bound schema rejected", err)
	}

	otherCandidate := strings.Repeat("b", 64)
	otherSchema, err := writercontract.UTF8SchemaForCandidate(otherCandidate)
	if err != nil {
		t.Fatal(err)
	}
	wrongInput, _ := json.Marshal(map[string]any{"candidate_id": otherCandidate, "output_schema": schema, "instruction": "write"})
	wrongInvocation, err := runtime.NewInvocation(profile, string(wrongInput))
	if err != nil {
		t.Fatal(err)
	}
	wrongIntent := intent
	wrongIntent.Invocation = wrongInvocation
	if err := validateStructuredOutputIntent(wrongIntent); err == nil {
		t.Fatal("candidate-bound schema detached from invocation candidate was admitted")
	}

	shapeDrift := strings.Replace(string(otherSchema), `"minItems":1`, `"minItems":0`, 1)
	driftExpectation, err := opencode.NewStructuredOutputExpectation([]byte(shapeDrift))
	if err != nil {
		t.Fatal(err)
	}
	driftInput := `{"candidate_id":"` + otherCandidate + `","output_schema":` + shapeDrift + `,"instruction":"write"}`
	driftInvocation, err := runtime.NewInvocation(profile, driftInput)
	if err != nil {
		t.Fatal(err)
	}
	driftIntent := Intent{Version: 2, Invocation: driftInvocation, StructuredOutput: &driftExpectation}
	if err := validateStructuredOutputIntent(driftIntent); err == nil {
		t.Fatal("candidate-bound schema shape drift was admitted")
	}
}

func TestStructuredOutputIntentBindsCandidateSpecificAnchoredSchema(t *testing.T) {
	candidate := strings.Repeat("a", 64)
	schema, err := writercontract.AnchoredEditsSchemaForCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	profile := runtime.Profile{Runtime: "opencode-http", Provider: "engorch-openai", Model: "writer-model", Effort: "none", Role: "writer"}
	input, err := json.Marshal(map[string]any{"candidate_id": candidate, "output_schema": schema, "instruction": "edit anchors"})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(profile, string(input))
	if err != nil {
		t.Fatal(err)
	}
	expectation, err := opencode.NewStructuredOutputExpectation(schema)
	if err != nil {
		t.Fatal(err)
	}
	intent := Intent{Version: 2, Invocation: invocation, StructuredOutput: &expectation}
	if err := validateStructuredOutputIntent(intent); err != nil {
		t.Fatal("exact candidate-bound anchored schema rejected", err)
	}

	otherCandidate := strings.Repeat("b", 64)
	otherSchema, _ := writercontract.AnchoredEditsSchemaForCandidate(otherCandidate)
	wrongInput, _ := json.Marshal(map[string]any{"candidate_id": otherCandidate, "output_schema": schema, "instruction": "edit anchors"})
	wrongInvocation, err := runtime.NewInvocation(profile, string(wrongInput))
	if err != nil {
		t.Fatal(err)
	}
	wrongIntent := intent
	wrongIntent.Invocation = wrongInvocation
	if err := validateStructuredOutputIntent(wrongIntent); err == nil {
		t.Fatal("anchored schema detached from envelope candidate was admitted")
	}

	drift := strings.Replace(string(otherSchema), `"maxItems":64`, `"maxItems":63`, 1)
	driftExpectation, err := opencode.NewStructuredOutputExpectation([]byte(drift))
	if err != nil {
		t.Fatal(err)
	}
	driftInput := `{"candidate_id":"` + otherCandidate + `","output_schema":` + drift + `,"instruction":"edit anchors"}`
	driftInvocation, err := runtime.NewInvocation(profile, driftInput)
	if err != nil {
		t.Fatal(err)
	}
	driftIntent := Intent{Version: 2, Invocation: driftInvocation, StructuredOutput: &driftExpectation}
	if err := validateStructuredOutputIntent(driftIntent); err == nil {
		t.Fatal("anchored schema shape drift was admitted")
	}
}

func TestProviderTerminalStructuredOutputUsesSeparateRawSchemaDigest(t *testing.T) {
	expectation, err := opencode.NewStructuredOutputExpectation(writercontract.UTF8Schema())
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := ProviderTerminalStructuredOutputExpectation(&expectation)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(expectation.Schema)
	if terminal == nil || terminal.Name != providergateway.StructuredOutputToolName || !bytes.Equal(terminal.Schema, expectation.Schema) || terminal.SchemaSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("provider terminal expectation lost exact schema identity", terminal)
	}
	if terminal.SchemaSHA256 == expectation.SchemaSHA256 {
		t.Fatal("provider and OpenCode schema identity domains were conflated")
	}
}

func TestLegacyIntentDurableShapeOmitsNativeStructuredOutput(t *testing.T) {
	raw, err := canonical.Bytes(Intent{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"structured_output"`)) {
		t.Fatal("legacy intent acquired native structured output field")
	}
}
