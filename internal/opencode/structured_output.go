package opencode

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"harness.local/engorch/internal/canonical"
)

// StructuredOutputToolName is the stock OpenCode synthetic tool used for a
// native json_schema prompt. It is a local result channel, not a controller
// tool and never authorizes a broker call.
const StructuredOutputToolName = "StructuredOutput"

const structuredOutputExpectationVersion = 1
const maxStructuredOutputSchemaBytes = 64 << 10
const maxStructuredOutputValueBytes = 256 << 10

// StructuredOutputExpectation binds one exact schema to one opt-in OpenCode
// dispatch. RetryCount is recorded in the native format request for replay
// identity; stock OpenCode's independent session retry policy remains outside
// this value.
type StructuredOutputExpectation struct {
	Version      int             `json:"version"`
	Schema       json.RawMessage `json:"schema"`
	SchemaSHA256 string          `json:"schema_sha256"`
	RetryCount   int             `json:"retry_count"`
}

// StructuredOutputToolObservation records the synthetic terminal tool's
// identity and exact state digests separately from controller tool calls.
type StructuredOutputToolObservation struct {
	PartID          string `json:"part_id"`
	CallID          string `json:"call_id"`
	ArgumentsSHA256 string `json:"arguments_sha256"`
	ResultSHA256    string `json:"result_sha256"`
}

const structuredOutputResult = "Structured output captured successfully."

// NewStructuredOutputExpectation canonicalizes a bounded object schema and
// binds its digest. The role owner still decides which schema is authorized;
// this package only proves that the dispatched and observed schemas agree.
func NewStructuredOutputExpectation(schema json.RawMessage) (StructuredOutputExpectation, error) {
	var result StructuredOutputExpectation
	normalized, err := normalizeStructuredOutputSchema(schema)
	if err != nil {
		return result, err
	}
	result.Version = structuredOutputExpectationVersion
	result.Schema = append(json.RawMessage(nil), normalized...)
	result.RetryCount = 0
	result.SchemaSHA256, err = canonical.Hash("harness.opencode-structured-output-schema.v1", json.RawMessage(normalized))
	if err != nil {
		return StructuredOutputExpectation{}, err
	}
	return result, nil
}

// Validate proves the persisted expectation has not been substituted. A
// nonzero retry count is rejected because this bridge is deliberately one
// native structured-result contract; stock retry behavior is not represented
// as a successful structured result.
func (e StructuredOutputExpectation) Validate() error {
	if e.Version != structuredOutputExpectationVersion || e.RetryCount != 0 {
		return errors.New("invalid structured output expectation")
	}
	normalized, err := normalizeStructuredOutputSchema(e.Schema)
	if err != nil || !bytes.Equal(normalized, e.Schema) {
		return errors.New("structured output schema is not canonical")
	}
	if len(e.SchemaSHA256) != 64 {
		return errors.New("invalid structured output schema identity")
	}
	if _, err := hex.DecodeString(e.SchemaSHA256); err != nil || strings.ToLower(e.SchemaSHA256) != e.SchemaSHA256 {
		return errors.New("invalid structured output schema identity")
	}
	want, err := canonical.Hash("harness.opencode-structured-output-schema.v1", json.RawMessage(normalized))
	if err != nil || want != e.SchemaSHA256 {
		return errors.New("structured output schema identity mismatch")
	}
	return nil
}

// ID returns the durable identity of the complete expectation.
func (e StructuredOutputExpectation) ID() (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash("harness.opencode-structured-output-expectation.v1", e)
}

// PromptFormat returns the exact format object accepted by stock OpenCode.
func (e StructuredOutputExpectation) PromptFormat() (json.RawMessage, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	raw, err := canonical.Bytes(struct {
		Type       string          `json:"type"`
		Schema     json.RawMessage `json:"schema"`
		RetryCount int             `json:"retryCount"`
	}{Type: "json_schema", Schema: e.Schema, RetryCount: 0})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func normalizeStructuredOutputSchema(raw []byte) ([]byte, error) {
	if len(raw) > maxStructuredOutputSchemaBytes {
		return nil, errors.New("structured output schema exceeds bound")
	}
	normalized, err := canonical.Normalize(raw)
	if err != nil || len(normalized) < 2 || normalized[0] != '{' || normalized[len(normalized)-1] != '}' {
		return nil, errors.New("structured output object schema required")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(normalized, &object); err != nil || object == nil {
		return nil, errors.New("structured output object schema required")
	}
	var schemaType string
	if rawType, ok := object["type"]; !ok || json.Unmarshal(rawType, &schemaType) != nil || schemaType != "object" {
		return nil, errors.New("structured output object schema required")
	}
	return normalized, nil
}

func normalizeStructuredOutputValue(raw []byte) ([]byte, error) {
	if len(raw) > maxStructuredOutputValueBytes {
		return nil, errors.New("structured output value exceeds bound")
	}
	normalized, err := canonical.Normalize(raw)
	if err != nil || len(normalized) < 2 || normalized[0] != '{' || normalized[len(normalized)-1] != '}' {
		return nil, errors.New("structured output object required")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(normalized, &object); err != nil || object == nil {
		return nil, errors.New("structured output object required")
	}
	return normalized, nil
}
