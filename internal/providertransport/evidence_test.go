package providertransport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/providergateway"
)

func TestResponseEvidenceRetainsExactPrivateBytesBeforeDecode(t *testing.T) {
	fixture, call := responseEvidenceFixture(t)
	body := []byte("{\"credential_echo\":\"" + fixtureSecret + "\",\"broken\":")
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{
		"Content-Type":     []string{"application/json; charset=utf-8"},
		"Content-Encoding": []string{"identity"},
	}, TransferEncoding: []string{"chunked"}, Request: &http.Request{Header: http.Header{"Authorization": []string{"Bearer request-only-secret"}}}}
	evidence, err := captureResponseEvidence(fixture.request, call, response, body, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(evidence.directory, evidence.rawArtifact))
	if err != nil {
		t.Fatal(err)
	}
	var retained rawResponseEvidence
	if err := json.Unmarshal(raw, &retained); err != nil {
		t.Fatal(err)
	}
	bodyHash := sha256.Sum256(body)
	if string(retained.Body) != string(body) || retained.CaptureScope != "full-body" || retained.CapturedSHA256 != hex.EncodeToString(bodyHash[:]) || retained.FullBodySHA256 != retained.CapturedSHA256 {
		t.Fatal("raw response evidence did not retain exact full-body bytes", retained)
	}
	if retained.ContentType[0] != "application/json; charset=utf-8" || retained.ContentEncoding[0] != "identity" || retained.TransferEncoding[0] != "chunked" {
		t.Fatal("wire metadata was not retained", retained)
	}
	if strings.Contains(string(raw), "request-only-secret") {
		t.Fatal("request credential was copied into response evidence")
	}
	decodeErr := errors.New("invalid provider response")
	ref, err := evidence.recordFailure(classifyDecodeFailure(body, providergateway.ResponseFramingJSON, decodeErr), "decode", decodeErr)
	if err != nil {
		t.Fatal(err)
	}
	diagnostic, err := os.ReadFile(filepath.Join(evidence.directory, ref.Artifact))
	if err != nil {
		t.Fatal(err)
	}
	diagnosticHash := sha256.Sum256(diagnostic)
	var failure decoderFailureEvidence
	if err := json.Unmarshal(diagnostic, &failure); err != nil || ref.SHA256 != hex.EncodeToString(diagnosticHash[:]) || failure.Code != "INVALID_JSON" || failure.JSONSyntaxOffset == 0 || failure.Cause != decodeErr.Error() || failure.RawArtifactSHA256 != evidence.rawSHA256 {
		t.Fatal("precise decoder failure evidence mismatch", failure, ref, err)
	}
}

func TestResponseEvidenceOversizedCaptureIsPrefixOnly(t *testing.T) {
	fixture, call := responseEvidenceFixture(t)
	body := make([]byte, fixture.binding.Model.MaxResponseBytes+1)
	for index := range body {
		body[index] = byte(index)
	}
	evidence, err := captureResponseEvidence(fixture.request, call, &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}, body, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(evidence.directory, evidence.rawArtifact))
	if err != nil {
		t.Fatal(err)
	}
	var retained rawResponseEvidence
	if err := json.Unmarshal(raw, &retained); err != nil {
		t.Fatal(err)
	}
	if retained.CaptureScope != "prefix-incomplete" || retained.FullBodySHA256 != "" || retained.CapturedBytes != int64(len(body)) || string(retained.Body) != string(body) {
		t.Fatal("oversized prefix made an unsupported full-body claim", retained)
	}
	if _, err := captureResponseEvidence(fixture.request, call, &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}, append(body, 0), false); err == nil {
		t.Fatal("capture beyond max-response-plus-one was admitted")
	}
}

func TestResponseEvidenceRejectsTamperAndLinkedDirectory(t *testing.T) {
	fixture, call := responseEvidenceFixture(t)
	body := []byte("not-json")
	evidence, err := captureResponseEvidence(fixture.request, call, &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}, body, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evidence.directory, evidence.rawArtifact), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := evidence.recordFailure("INVALID_JSON", "decode", errors.New("decoder rejected")); err == nil {
		t.Fatal("tampered raw evidence admitted a diagnostic reference")
	}

	linkedFixture, linkedCall := responseEvidenceFixture(t)
	external := t.TempDir()
	linkedDirectory := linkedFixture.gatewayPath + ".response-evidence"
	if err := os.Symlink(external, linkedDirectory); err != nil {
		t.Skipf("host cannot create a directory symlink: %v", err)
	}
	if _, err := captureResponseEvidence(linkedFixture.request, linkedCall, &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}, body, true); err == nil {
		t.Fatal("linked evidence directory was admitted")
	}
	if entries, err := os.ReadDir(external); err != nil || len(entries) != 0 {
		t.Fatal("linked external directory was mutated", entries, err)
	}
}

func TestDecodeFailureClassificationUsesTypedJSONDiagnostics(t *testing.T) {
	syntax := &json.SyntaxError{Offset: 7}
	if got := classifyDecodeFailure([]byte(`{"x":`), providergateway.ResponseFramingJSON, syntax); got != "INVALID_JSON" {
		t.Fatal("typed syntax error lost", got)
	}
	mismatch := &json.UnmarshalTypeError{Value: "string", Type: nil, Offset: 12, Struct: "envelope", Field: "usage.input_tokens"}
	if got := classifyDecodeFailure([]byte(`{"usage":{"input_tokens":"x"}}`), providergateway.ResponseFramingJSON, mismatch); got != "SCHEMA_MISMATCH" {
		t.Fatal("typed schema mismatch lost", got)
	}
	record := decoderFailureEvidence{Framing: providergateway.ResponseFramingJSON}
	attachJSONDiagnostic(&record, nil, mismatch)
	if record.JSONTypeOffset != 12 || record.JSONStruct != "envelope" || record.JSONField != "usage.input_tokens" {
		t.Fatal("typed JSON diagnostic fields lost", record)
	}
	if got := classifyDecodeFailure(nil, "", errors.New("provider SSE is incomplete")); got != "SSE_TERMINAL_MISSING" {
		t.Fatal("legacy empty framing did not resolve to SSE", got)
	}
}

func responseEvidenceFixture(t *testing.T) (transportFixture, providergateway.CallIntent) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	fixture := newFiniteTransportFixture(t, server, providergateway.OpenAIChatCompletionsAdapter)
	metadata, err := providergateway.ValidateAdapterRequest(fixture.request.Body, fixture.binding, fixture.request.Expectation)
	if err != nil {
		t.Fatal(err)
	}
	call, err := providergateway.BeginWithExpectation(fixture.gatewayPath, fixture.accessPath, fixture.policy, fixture.intent, fixture.binding, metadata.SHA256, metadata.SizeBytes, fixture.request.Expectation)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, call
}
