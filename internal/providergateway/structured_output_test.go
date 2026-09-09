package providergateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateStructuredOutputJSONOnlyAndStrictSchema(t *testing.T) {
	jsonOnly := &RequiredCapabilities{StructuredOutput: StructuredOutputJSONOnly}
	if err := validateStructuredOutput(`{"answer":"yes"}`, jsonOnly); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{`not json`, `{"answer":1,"answer":2}`, `{"answer":1} trailing`} {
		if err := validateStructuredOutput(invalid, jsonOnly); err == nil {
			t.Fatal("invalid or ambiguous JSON admitted", invalid)
		}
	}

	strict := &RequiredCapabilities{
		StructuredOutput: StructuredOutputStrictSchema,
		SchemaName:       "answer",
		Schema:           json.RawMessage(`{"additionalProperties":false,"properties":{"answer":{"type":"string"}},"required":["answer"],"type":"object"}`),
	}
	if err := validateStructuredOutput(`{"answer":"yes"}`, strict); err != nil {
		t.Fatal(err)
	}
	if err := validateStructuredOutput(`{"answer":1}`, strict); err == nil {
		t.Fatal("schema-violating provider output admitted")
	}
}

func TestStructuredSchemaIsBoundedAndCannotLoadReferences(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"$ref":"https://outside.test/schema"}`),
		json.RawMessage(`{"$ref":"file:///secret/schema.json"}`),
		json.RawMessage(`{"type":"object","type":"array"}`),
		json.RawMessage(`{"description":"` + strings.Repeat("x", maximumStructuredSchemaBytes) + `"}`),
	} {
		if err := validateStructuredOutputSchema(raw); err == nil {
			t.Fatal("unsafe structured output schema admitted")
		}
	}
}

func TestDecodeResponsesAdapterValidatesStrictOutputAtProductionBoundary(t *testing.T) {
	capabilities := responsesCapabilities(false, false)
	capabilities.TextFormats = []string{"json_object", "json_schema", "plain"}
	binding := responsesBinding(t, capabilities)
	binding.Model.ObservedModelAliases = []string{"provider-model"}
	binding.ModelID, _ = binding.Model.ID()
	schema := json.RawMessage(`{"additionalProperties":false,"properties":{"answer":{"type":"string"}},"required":["answer"],"type":"object"}`)
	required := &RequiredCapabilities{StructuredOutput: StructuredOutputStrictSchema, SchemaName: "answer", Schema: schema}
	raw := string(responsesTextFixture())
	raw = strings.ReplaceAll(raw, "Salut, ", "{\\\"answer\\\":\\\"")
	raw = strings.ReplaceAll(raw, "lume!", "yes\\\"}")
	expected := AdapterResponseExpectation{MaxBytes: len(raw), MaxTotalTokens: 10, RequiredCapabilities: required}
	got, err := DecodeAdapterResponse([]byte(raw), binding, expected)
	if err != nil || got.Content == nil || got.Content.OutputText != `{"answer":"yes"}` {
		t.Fatal("strict structured output was not returned through the decoder", got.Content, err)
	}
	invalid := strings.ReplaceAll(raw, `\"yes\"`, `1`)
	if _, err := DecodeAdapterResponse([]byte(invalid), binding, AdapterResponseExpectation{MaxBytes: len(invalid), MaxTotalTokens: 10, RequiredCapabilities: required}); err == nil {
		t.Fatal("schema-violating adapter output was admitted")
	}
}
