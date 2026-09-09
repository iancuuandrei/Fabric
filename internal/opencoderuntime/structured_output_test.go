package opencoderuntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/writercontract"
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
