package providergateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This test is opt-in because the response body is restricted evidence and
// must never be copied into the repository or printed by a failing assertion.
func TestParseResponsesSSEPrivateM2hIncompleteResponse(t *testing.T) {
	responsePath := os.Getenv("ENGORCH_PRIVATE_M2H_RESPONSE_ARTIFACT")
	if responsePath == "" {
		t.Skip("set ENGORCH_PRIVATE_M2H_RESPONSE_ARTIFACT for the restricted exact replay")
	}
	encoded, err := os.ReadFile(responsePath)
	if err != nil {
		t.Fatal(err)
	}
	var artifact struct {
		CapturedBytes  int    `json:"captured_bytes"`
		CapturedSHA256 string `json:"captured_sha256"`
		FullBodySHA256 string `json:"full_body_sha256"`
		HTTPStatus     int    `json:"http_status"`
		Framing        string `json:"framing"`
		CaptureScope   string `json:"capture_scope"`
		Body           []byte `json:"body"`
	}
	if err := json.Unmarshal(encoded, &artifact); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(artifact.Body)
	bodySHA256 := hex.EncodeToString(digest[:])
	if artifact.CapturedBytes != 386937 || len(artifact.Body) != 386937 || artifact.CapturedSHA256 != "9204c17570494e5c5ab2802cd5e870eeeadc045eef867d7b63a56c4863501f3c" || artifact.FullBodySHA256 != artifact.CapturedSHA256 || bodySHA256 != artifact.CapturedSHA256 || artifact.HTTPStatus != 200 || artifact.Framing != "sse" || artifact.CaptureScope != "full-body" {
		t.Fatal("restricted response capture identity or transport shape changed", artifact.CapturedBytes, len(artifact.Body), artifact.CapturedSHA256, artifact.FullBodySHA256, artifact.HTTPStatus, artifact.Framing, artifact.CaptureScope)
	}

	failurePath := strings.TrimSuffix(responsePath, ".response.json") + ".failure.json"
	failureEncoded, err := os.ReadFile(filepath.Clean(failurePath))
	if err != nil {
		t.Fatal("paired decoder failure artifact unavailable", err)
	}
	var failure struct {
		CallID            string `json:"call_id"`
		RawArtifact       string `json:"raw_artifact"`
		RawArtifactSHA256 string `json:"raw_artifact_sha256"`
		Code              string `json:"code"`
		Stage             string `json:"stage"`
		Cause             string `json:"cause"`
	}
	if err := json.Unmarshal(failureEncoded, &failure); err != nil {
		t.Fatal(err)
	}
	artifactFileDigest := sha256.Sum256(encoded)
	if failure.CallID == "" || failure.RawArtifact != filepath.Base(responsePath) || failure.RawArtifactSHA256 != hex.EncodeToString(artifactFileDigest[:]) || failure.Code != "DECODER_REJECTED" || failure.Stage != "adapter" || failure.Cause != "provider Responses stream lacks required trailing cost ping" {
		t.Fatal("paired decoder failure metadata changed", failure.RawArtifact, failure.RawArtifactSHA256, failure.Code, failure.Stage, failure.Cause)
	}

	_, err = ParseResponsesSSEWithOptions(artifact.Body, len(artifact.Body), 1<<20, ResponsesSSEOptions{RequireTrailingCostPingV1: true})
	var terminal *ResponsesTerminalError
	if !errors.As(err, &terminal) || terminal.Status != "incomplete" {
		t.Fatal("required-ping replay did not preserve the incomplete terminal", err)
	}
	_, err = ParseResponsesSSEWithOptions(artifact.Body, len(artifact.Body), 1<<20, ResponsesSSEOptions{})
	if !errors.As(err, &terminal) || terminal.Status != "incomplete" {
		t.Fatal("optional-ping replay did not prove the terminal response was incomplete", err)
	}

	completed := responsesTextFixture()
	_, err = ParseResponsesSSEWithOptions(completed, len(completed), 10, ResponsesSSEOptions{RequireTrailingCostPingV1: true})
	if err == nil || err.Error() != "provider Responses stream lacks required trailing cost ping" {
		t.Fatal("completed response without required ping was admitted", err)
	}
}
