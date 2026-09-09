package providertransport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/safepath"
)

const (
	responseEvidenceVersion = 1
	maximumDiagnosticBytes  = 64 << 10
)

// responseEvidence is a private, immutable handle to the exact bytes captured
// from one admitted response. Its files may contain provider content or echoed
// credentials and must never be copied into the gateway journal or model input.
type responseEvidence struct {
	directory   string
	callID      string
	rawArtifact string
	rawSHA256   string
	rawBytes    int64
	adapter     string
	framing     providergateway.ResponseFraming
	body        []byte
}

type rawResponseEvidence struct {
	Version          int                             `json:"version"`
	CallID           string                          `json:"call_id"`
	Adapter          string                          `json:"adapter"`
	Decoder          string                          `json:"decoder"`
	Framing          providergateway.ResponseFraming `json:"framing"`
	HTTPStatus       int                             `json:"http_status"`
	ContentType      []string                        `json:"content_type"`
	ContentEncoding  []string                        `json:"content_encoding"`
	TransferEncoding []string                        `json:"transfer_encoding"`
	CaptureScope     string                          `json:"capture_scope"`
	CapturedBytes    int64                           `json:"captured_bytes"`
	CapturedSHA256   string                          `json:"captured_sha256"`
	FullBodySHA256   string                          `json:"full_body_sha256,omitempty"`
	Body             []byte                          `json:"body"`
}

type decoderFailureEvidence struct {
	Version           int                             `json:"version"`
	CallID            string                          `json:"call_id"`
	RawArtifact       string                          `json:"raw_artifact"`
	RawArtifactSHA256 string                          `json:"raw_artifact_sha256"`
	Adapter           string                          `json:"adapter"`
	Framing           providergateway.ResponseFraming `json:"framing"`
	Code              string                          `json:"code"`
	Stage             string                          `json:"stage"`
	Cause             string                          `json:"cause"`
	JSONSyntaxOffset  int64                           `json:"json_syntax_offset,omitempty"`
	JSONTypeOffset    int64                           `json:"json_type_offset,omitempty"`
	JSONField         string                          `json:"json_field,omitempty"`
	JSONStruct        string                          `json:"json_struct,omitempty"`
}

// captureResponseEvidence persists the bounded wire observation before any
// response decoder is invoked. complete may be true only when EOF was observed;
// an incomplete capture binds its prefix but makes no full-body hash claim.
func captureResponseEvidence(request Request, call providergateway.CallIntent, response *http.Response, body []byte, complete bool) (*responseEvidence, error) {
	if response == nil || !filepath.IsAbs(request.GatewayJournalPath) || safepath.RequireDigest(call.CallID) != nil {
		return nil, errors.New("invalid provider response evidence identity")
	}
	callID, err := call.ID()
	bindingID, bindingErr := request.Binding.ID()
	if err != nil || bindingErr != nil || callID != call.CallID || call.BindingID != bindingID || call.InvocationID != request.Binding.AccessInvocationID {
		return nil, errors.New("provider response evidence differs from admitted call")
	}
	limit := request.Binding.Model.MaxResponseBytes
	if limit < 1 || int64(len(body)) > limit+1 || complete && int64(len(body)) > limit {
		return nil, errors.New("provider response evidence exceeds capture bound")
	}
	framing, err := providergateway.EffectiveResponseFraming(request.Expectation.ResponseFraming)
	if err != nil {
		return nil, err
	}
	capturedDigest := sha256.Sum256(body)
	capturedSHA256 := hex.EncodeToString(capturedDigest[:])
	scope := "prefix-incomplete"
	fullSHA256 := ""
	if complete {
		scope = "full-body"
		fullSHA256 = capturedSHA256
	}
	record := rawResponseEvidence{
		Version: responseEvidenceVersion, CallID: call.CallID,
		Adapter: request.Binding.Model.AdapterID, Decoder: request.Binding.Model.AdapterID + "/" + string(framing), Framing: framing,
		HTTPStatus: response.StatusCode, ContentType: cloneHeaderValues(response.Header.Values("Content-Type")),
		ContentEncoding: cloneHeaderValues(response.Header.Values("Content-Encoding")), TransferEncoding: append([]string(nil), response.TransferEncoding...),
		CaptureScope: scope, CapturedBytes: int64(len(body)), CapturedSHA256: capturedSHA256, FullBodySHA256: fullSHA256,
		Body: append([]byte(nil), body...),
	}
	artifactBytes, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	directory, root, err := openEvidenceDirectory(request.GatewayJournalPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	artifact := call.CallID + ".response.json"
	artifactSHA256, err := publishPrivateEvidence(root, artifact, artifactBytes, int64(len(artifactBytes)))
	if err != nil {
		return nil, err
	}
	return &responseEvidence{directory: directory, callID: call.CallID, rawArtifact: artifact, rawSHA256: artifactSHA256, rawBytes: int64(len(artifactBytes)), adapter: record.Adapter, framing: framing, body: append([]byte(nil), body...)}, nil
}

// recordFailure stores the exact decoder diagnostic privately and returns only
// its immutable basename and digest for the bounded gateway observation.
func (e *responseEvidence) recordFailure(code, stage string, cause error) (*providergateway.FailureEvidenceRef, error) {
	if e == nil || cause == nil || !diagnosticToken(code) || !diagnosticToken(stage) || safepath.RequireDigest(e.callID) != nil || safepath.RequireDigest(e.rawSHA256) != nil || filepath.Base(e.rawArtifact) != e.rawArtifact {
		return nil, errors.New("invalid provider decoder failure evidence")
	}
	causeText := cause.Error()
	if causeText == "" || len(causeText) > maximumDiagnosticBytes {
		return nil, errors.New("provider decoder diagnostic exceeds bound")
	}
	record := decoderFailureEvidence{Version: responseEvidenceVersion, CallID: e.callID, RawArtifact: e.rawArtifact, RawArtifactSHA256: e.rawSHA256, Adapter: e.adapter, Framing: e.framing, Code: code, Stage: stage, Cause: causeText}
	attachJSONDiagnostic(&record, e.body, cause)
	parts := strings.SplitN(e.rawArtifact, ".", 2)
	if len(parts) != 2 {
		return nil, errors.New("invalid raw provider evidence name")
	}
	if err := safepath.Directory(e.directory); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(e.directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	digest, size, executable, exists, err := safepath.ReadRegular(root, e.rawArtifact, e.rawBytes)
	if err != nil || !exists || executable || size != e.rawBytes || digest != e.rawSHA256 {
		return nil, errors.New("raw provider response evidence changed before diagnostic publication")
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	artifact := e.callID + ".failure.json"
	digest, err = publishPrivateEvidence(root, artifact, raw, int64(len(raw)))
	if err != nil {
		return nil, err
	}
	return &providergateway.FailureEvidenceRef{Artifact: artifact, SHA256: digest}, nil
}

// classifyDecodeFailure gives a finite diagnostic category without changing
// whether the provider response is accepted. The exact decoder error remains in
// the private failure artifact.
func classifyDecodeFailure(raw []byte, framing providergateway.ResponseFraming, err error) string {
	if err == nil {
		return "RESPONSE_CONTRACT_REJECTED"
	}
	if errors.Is(err, providergateway.ErrCapabilityUnavailable) {
		return "CAPABILITY_UNAVAILABLE"
	}
	var anthropic *providergateway.AnthropicTerminalError
	if errors.As(err, &anthropic) {
		return "ANTHROPIC_TERMINAL_" + diagnosticStatus(anthropic.Status)
	}
	var responses *providergateway.ResponsesTerminalError
	if errors.As(err, &responses) {
		return "RESPONSES_TERMINAL_" + diagnosticStatus(responses.Status)
	}
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return "INVALID_JSON"
	}
	var mismatch *json.UnmarshalTypeError
	if errors.As(err, &mismatch) {
		return "SCHEMA_MISMATCH"
	}
	if effective, framingErr := providergateway.EffectiveResponseFraming(framing); framingErr == nil {
		framing = effective
	}
	if framing == providergateway.ResponseFramingJSON && !json.Valid(raw) {
		return "INVALID_JSON"
	}
	switch err.Error() {
	case "invalid provider chat completion", "invalid completed provider Responses state", "invalid completed provider Responses object", "invalid Anthropic Messages object":
		return "SCHEMA_MISMATCH"
	}
	if framing == providergateway.ResponseFramingSSE {
		switch err.Error() {
		case "invalid provider SSE line ending", "unterminated provider SSE event", "invalid provider Responses SSE line ending", "unterminated provider Responses SSE event", "invalid Anthropic SSE line ending", "unterminated Anthropic SSE event":
			return "INVALID_SSE_FRAMING"
		case "provider SSE is incomplete", "provider SSE lacks role, finish or complete usage", "provider Responses SSE is incomplete", "provider Responses SSE lacks completed terminal", "Anthropic Messages SSE is incomplete", "Anthropic Messages SSE lacks message_stop":
			return "SSE_TERMINAL_MISSING"
		}
	}
	return "DECODER_REJECTED"
}

func attachJSONDiagnostic(record *decoderFailureEvidence, raw []byte, cause error) {
	var syntax *json.SyntaxError
	if errors.As(cause, &syntax) {
		record.JSONSyntaxOffset = syntax.Offset
		return
	}
	var mismatch *json.UnmarshalTypeError
	if errors.As(cause, &mismatch) {
		record.JSONTypeOffset = mismatch.Offset
		record.JSONField = mismatch.Field
		record.JSONStruct = mismatch.Struct
		return
	}
	if record.Framing != providergateway.ResponseFramingJSON || json.Valid(raw) {
		return
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil && errors.As(err, &syntax) {
		record.JSONSyntaxOffset = syntax.Offset
	}
}

func openEvidenceDirectory(gatewayPath string) (string, *os.Root, error) {
	parent := filepath.Dir(gatewayPath)
	name := filepath.Base(gatewayPath) + ".response-evidence"
	if err := safepath.Directory(parent); err != nil {
		return "", nil, err
	}
	if err := safepath.Relative(name); err != nil {
		return "", nil, err
	}
	if err := safepath.EnsureDirectory(parent, name); err != nil {
		return "", nil, err
	}
	directory := filepath.Join(parent, name)
	if err := safepath.Directory(directory); err != nil {
		return "", nil, err
	}
	root, err := os.OpenRoot(directory)
	return directory, root, err
}

func publishPrivateEvidence(root *os.Root, name string, data []byte, limit int64) (string, error) {
	if root == nil || safepath.Relative(name) != nil || len(data) == 0 || int64(len(data)) > limit {
		return "", errors.New("invalid private provider evidence")
	}
	want := sha256.Sum256(data)
	wantDigest := hex.EncodeToString(want[:])
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		n, writeErr := file.Write(data)
		if writeErr == nil && n != len(data) {
			writeErr = errors.New("short private evidence write")
		}
		syncErr := file.Sync()
		closeErr := file.Close()
		if joined := errors.Join(writeErr, syncErr, closeErr); joined != nil {
			return "", joined
		}
	} else if !errors.Is(err, os.ErrExist) {
		return "", err
	}
	digest, size, executable, exists, err := safepath.ReadRegular(root, name, limit)
	if err != nil || !exists || executable || size != int64(len(data)) || digest != wantDigest {
		return "", fmt.Errorf("private provider evidence differs: %w", err)
	}
	return digest, nil
}

func cloneHeaderValues(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string(nil), values...)
}

func diagnosticToken(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func diagnosticStatus(status string) string {
	status = strings.ToUpper(strings.ReplaceAll(status, "-", "_"))
	if !diagnosticToken(status) {
		return "ERROR"
	}
	return status
}
