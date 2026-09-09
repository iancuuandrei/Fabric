package providertransport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/providergateway"
)

func TestExecuteValidationRejectionStoresBoundedEvidenceWithoutGatewayEffect(t *testing.T) {
	calls := 0
	server := newTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write(validResponsesToolSSE("provider-wire-model"))
	})
	fixture := newResponsesTransportFixture(t, server)
	t.Setenv("HTTPS_PROXY", "https://127.0.0.1:1")
	ctx, cancel := contextWithTimeout(t)
	defer cancel()

	if result, err := fixture.client.Execute(ctx, fixture.request); err != nil || result.Receipt.Finish != "tool_calls" {
		t.Fatal("first request did not complete", result, err)
	}
	before, err := providergateway.Inspect(fixture.gatewayPath)
	if err != nil {
		t.Fatal(err)
	}
	accessBefore, err := journal.Read(fixture.accessPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.Finished || before.Pending != nil || len(before.Calls) != 1 || before.Calls[0].Receipt == nil || before.Calls[0].Receipt.Finish != "tool_calls" {
		t.Fatal("first tool response did not leave a resumable gateway state", before)
	}
	malformed := []byte(`{"model":"wire-model","input":[`)
	second := fixture.request
	second.Body = malformed
	result, rejectionErr := fixture.client.Execute(ctx, second)
	if !errors.Is(rejectionErr, ErrRejected) || len(result.Body) != 0 {
		t.Fatal("malformed second request was not rejected before admission", result, rejectionErr)
	}
	if calls != 1 {
		t.Fatal("validation rejection reached the provider", calls)
	}
	after, err := providergateway.Inspect(fixture.gatewayPath)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("validation rejection changed the gateway journal", before, after, err)
	}
	accessAfter, err := journal.Read(fixture.accessPath)
	if err != nil || !reflect.DeepEqual(accessBefore, accessAfter) {
		t.Fatal("validation rejection changed access settlement", accessBefore, accessAfter, err)
	}

	evidence, artifact, encoded := readSinglePreflightArtifact(t, fixture.gatewayPath)
	if evidence.Phase != preflightValidatePhase || string(evidence.Body) != string(malformed) || evidence.RequestBytes != int64(len(malformed)) || evidence.Cause != "invalid Responses request admission bounds, binding, capabilities or JSON" {
		t.Fatal("validation diagnostic lost exact bounded cause or body", evidence)
	}
	bindingID, err := fixture.binding.ID()
	if err != nil {
		t.Fatal(err)
	}
	if evidence.BindingID != bindingID || evidence.InvocationID != fixture.binding.AccessInvocationID || evidence.DiagnosticID == "" || evidence.DiagnosticID == after.Calls[0].Intent.CallID {
		t.Fatal("validation diagnostic identity was not bound independently of provider call ID", evidence)
	}
	digest := sha256.Sum256(malformed)
	if evidence.RequestSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("validation diagnostic request digest differs from exact body", evidence.RequestSHA256)
	}
	if !strings.HasPrefix(filepath.Base(artifact), "preflight-") || !strings.HasSuffix(filepath.Base(artifact), ".request.json") || strings.Contains(string(encoded), "call_id") || strings.Contains(string(encoded), fixtureSecret) || strings.Contains(strings.ToLower(string(encoded)), "authorization") {
		t.Fatal("private diagnostic leaked provider identity or credentials", artifact, string(encoded))
	}
	if evidence.JournalHeadSHA256 != journalHead(t, fixture.gatewayPath) {
		t.Fatal("validation diagnostic is not tied to the stable journal head", evidence)
	}
	if strings.Contains(rejectionErr.Error(), string(malformed)) {
		t.Fatal("public validation error exposed request bytes")
	}
}

func TestExecuteBeginRejectionStoresEvidenceWithoutSecondIntentOrUsage(t *testing.T) {
	calls := 0
	server := newTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write(validChatSSE())
	})
	fixture := newTransportFixture(t, server, "api", "bearer", "", 4096)
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	if _, err := fixture.client.Execute(ctx, fixture.request); err != nil {
		t.Fatal("first request did not complete", err)
	}
	before, err := providergateway.Inspect(fixture.gatewayPath)
	if err != nil {
		t.Fatal(err)
	}
	accessBefore, err := journal.Read(fixture.accessPath)
	if err != nil {
		t.Fatal(err)
	}
	result, rejectionErr := fixture.client.Execute(ctx, fixture.request)
	if !errors.Is(rejectionErr, ErrRejected) || len(result.Body) != 0 || rejectionErr.Error() != ErrRejected.Error() {
		t.Fatal("no-effect Begin rejection was not surfaced as a sanitized rejection", result, rejectionErr)
	}
	if calls != 1 {
		t.Fatal("Begin rejection reached the provider", calls)
	}
	after, err := providergateway.Inspect(fixture.gatewayPath)
	if err != nil || !reflect.DeepEqual(before, after) || len(after.Calls) != 1 || after.Calls[0].Receipt == nil || after.Aggregate != before.Aggregate {
		t.Fatal("Begin rejection changed calls, receipt, or usage", before, after, err)
	}
	accessAfter, err := journal.Read(fixture.accessPath)
	if err != nil || !reflect.DeepEqual(accessBefore, accessAfter) {
		t.Fatal("Begin rejection changed access settlement", accessBefore, accessAfter, err)
	}
	evidence, _, _ := readSinglePreflightArtifact(t, fixture.gatewayPath)
	if evidence.Phase != preflightBeginPhase || evidence.Cause == "" || evidence.DiagnosticID == after.Calls[0].Intent.CallID {
		t.Fatal("Begin rejection diagnostic was incomplete or confused with a call ID", evidence)
	}
	if strings.Contains(evidence.Cause, fixtureSecret) {
		t.Fatal("Begin rejection diagnostic retained a credential", evidence.Cause)
	}
}

func TestExecuteOversizePreflightBodyFailsClosedWithoutArtifact(t *testing.T) {
	calls := 0
	server := newTLSServer(t, func(http.ResponseWriter, *http.Request) { calls++ })
	fixture := newTransportFixture(t, server, "api", "bearer", "", 4096)
	request := fixture.request
	request.Body = make([]byte, maximumPreflightRequestBytes+1)
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	if result, err := fixture.client.Execute(ctx, request); !errors.Is(err, ErrPending) || len(result.Body) != 0 {
		t.Fatal("oversize diagnostic did not fail closed", result, err)
	}
	if calls != 0 {
		t.Fatal("oversize request reached the provider", calls)
	}
	state, err := providergateway.Inspect(fixture.gatewayPath)
	if err != nil || state.Pending != nil || len(state.Calls) != 0 {
		t.Fatal("oversize preflight changed the gateway", state, err)
	}
	if _, err := os.Stat(fixture.gatewayPath + ".preflight-evidence"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oversize request created an unbounded evidence directory", err)
	}
}

func TestPreflightEvidenceDuplicateAndTamperAreFailClosed(t *testing.T) {
	server := newTLSServer(t, func(http.ResponseWriter, *http.Request) {})
	fixture := newTransportFixture(t, server, "api", "bearer", "", 4096)
	observed, err := inspectPreflightJournal(fixture.gatewayPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("provider request admission failed")
	if err := recordPreflightRejection(fixture.request, observed, preflightValidatePhase, cause); err != nil {
		t.Fatal("first private diagnostic write failed", err)
	}
	if err := recordPreflightRejection(fixture.request, observed, preflightValidatePhase, cause); err != nil {
		t.Fatal("same diagnostic was not idempotent", err)
	}
	_, artifact, _ := readSinglePreflightArtifact(t, fixture.gatewayPath)
	if err := os.WriteFile(artifact, []byte(`tampered`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := recordPreflightRejection(fixture.request, observed, preflightValidatePhase, cause); err == nil {
		t.Fatal("tampered private diagnostic was silently accepted")
	}
}

func TestPreflightEvidenceRejectsDestinationAlias(t *testing.T) {
	server := newTLSServer(t, func(http.ResponseWriter, *http.Request) {})
	fixture := newTransportFixture(t, server, "api", "bearer", "", 4096)
	observed, err := inspectPreflightJournal(fixture.gatewayPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	alias := fixture.gatewayPath + ".preflight-evidence"
	if err := os.WriteFile(alias, []byte("regular-file-alias"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := recordPreflightRejection(fixture.request, observed, preflightValidatePhase, errors.New("provider request admission failed")); err == nil {
		t.Fatal("regular-file evidence destination alias was followed")
	}
}

func readSinglePreflightArtifact(t *testing.T, gatewayPath string) (preflightRejectionEvidence, string, []byte) {
	t.Helper()
	directory := gatewayPath + ".preflight-evidence"
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one private preflight artifact: %v (%d entries)", err, len(entries))
	}
	artifact := filepath.Join(directory, entries[0].Name())
	encoded, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	var evidence preflightRejectionEvidence
	if err := json.Unmarshal(encoded, &evidence); err != nil {
		t.Fatal(err)
	}
	return evidence, artifact, encoded
}

func journalHead(t *testing.T, path string) string {
	t.Helper()
	events, err := journal.Read(path)
	if err != nil || len(events) == 0 {
		t.Fatal("journal head unavailable", err)
	}
	return events[len(events)-1].Hash
}

func contextWithTimeout(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 5*time.Second)
}
