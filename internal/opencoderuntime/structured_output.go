package opencoderuntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/opencode"
	"github.com/iancuuandrei/fabric/internal/providergateway"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

// validateStructuredOutputIntent proves that the native terminal contract is
// the exact writer contract carried by the invocation. The controller owns
// opt in; this runtime check prevents a journal or caller from substituting a
// schema, role, or composite route after admission.
func validateStructuredOutputIntent(intent Intent) error {
	if intent.StructuredOutput == nil {
		return nil
	}
	if intent.Version != 2 || intent.Invocation.Version != 1 || intent.Invocation.Profile.Runtime != "opencode-http" || intent.Invocation.Profile.Role != "writer" && intent.Invocation.Profile.Role != "fixer" {
		return errors.New("native structured output is restricted to writer/fixer intents")
	}
	if err := intent.StructuredOutput.Validate(); err != nil {
		return err
	}
	wantUTF8, err := canonical.Normalize(writercontract.UTF8Schema())
	if err != nil {
		return err
	}
	wantJSON, err := canonical.Normalize(writercontract.ChangesJSONSchema())
	if err != nil {
		return err
	}
	var request struct {
		OutputSchema json.RawMessage `json:"output_schema"`
		CandidateID  string          `json:"candidate_id"`
	}
	if err := json.Unmarshal([]byte(intent.Invocation.Input), &request); err != nil || len(request.OutputSchema) == 0 || bytes.Equal(request.OutputSchema, []byte("null")) {
		return errors.New("native structured output schema missing from invocation")
	}
	got, err := canonical.Normalize(request.OutputSchema)
	allowed := bytes.Equal(got, wantUTF8) || bytes.Equal(got, wantJSON)
	if candidateSchema, schemaErr := writercontract.UTF8SchemaForCandidate(request.CandidateID); schemaErr == nil {
		wantCandidate, normalizeErr := canonical.Normalize(candidateSchema)
		allowed = allowed || normalizeErr == nil && bytes.Equal(got, wantCandidate)
	}
	if candidateSchema, schemaErr := writercontract.AnchoredEditsSchemaForCandidate(request.CandidateID); schemaErr == nil {
		wantCandidate, normalizeErr := canonical.Normalize(candidateSchema)
		allowed = allowed || normalizeErr == nil && bytes.Equal(got, wantCandidate)
	}
	if err != nil || !bytes.Equal(got, intent.StructuredOutput.Schema) || !allowed {
		return errors.New("native structured output invocation schema differs from contract")
	}
	return nil
}

// terminalStructuredOutputExpectation converts the OpenCode-domain contract
// into the provider gateway's separate terminal-tool identity. The two
// schemas are compared by bytes; their identity digests intentionally have
// different domains.
func terminalStructuredOutputExpectation(expectation *opencode.StructuredOutputExpectation) (*providergateway.TerminalStructuredOutputExpectation, error) {
	if expectation == nil {
		return nil, nil
	}
	if err := expectation.Validate(); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(expectation.Schema)
	return &providergateway.TerminalStructuredOutputExpectation{
		Version:      1,
		Name:         providergateway.StructuredOutputToolName,
		Schema:       append([]byte(nil), expectation.Schema...),
		SchemaSHA256: hex.EncodeToString(digest[:]),
	}, nil
}

// ProviderTerminalStructuredOutputExpectation exposes the provider-facing
// terminal identity needed by the controller's Responses proxy. It remains a
// separate identity domain from the OpenCode expectation.
func ProviderTerminalStructuredOutputExpectation(expectation *opencode.StructuredOutputExpectation) (*providergateway.TerminalStructuredOutputExpectation, error) {
	return terminalStructuredOutputExpectation(expectation)
}
