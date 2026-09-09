package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
)

const (
	// TransportFailureEvidenceVersion is the only accepted evidence schema.
	TransportFailureEvidenceVersion = 1

	// TransportFailurePhaseMessagePostWait identifies the one synchronous POST
	// whose transport loss leaves dispatch unresolved.
	TransportFailurePhaseMessagePostWait = "MESSAGE_POST_WAIT"
	// TransportFailurePhaseReadbackWait identifies bounded non-message traffic.
	TransportFailurePhaseReadbackWait = "READBACK_WAIT"

	// TransportDispatchStateUnknown never proves that a message was dispatched;
	// it only forbids treating an attempted request as conclusively unsent.
	TransportDispatchStateUnknown = "UNKNOWN"
	// TransportDispatchStateNotDispatched is limited to failures observed before
	// the HTTP client is allowed to perform network I/O.
	TransportDispatchStateNotDispatched = "NOT_DISPATCHED"

	maxTransportErrorBytes = 1024
)

// TransportFailureEvidence is a safe, bounded projection of a failed HTTP
// operation. It preserves caller cancellation separately from the underlying
// transport cause and grants no permission to retry a message POST.
type TransportFailureEvidence struct {
	Version          int    `json:"version"`
	TransportError   string `json:"transport_error"`
	ContextError     string `json:"context_error,omitempty"`
	Phase            string `json:"phase"`
	ElapsedMillis    int64  `json:"elapsed_ms"`
	Endpoint         string `json:"endpoint"`
	HTTPMethod       string `json:"http_method"`
	SessionID        string `json:"session_id,omitempty"`
	RequestMessageID string `json:"request_message_id,omitempty"`
	DispatchState    string `json:"dispatch_state"`
}

// Validate rejects unknown states and unsafe strings before evidence is made
// durable or used for recovery decisions.
func (e TransportFailureEvidence) Validate() error {
	if e.Version != TransportFailureEvidenceVersion || !safeTransportEvidenceText(e.TransportError, maxTransportErrorBytes) || e.ElapsedMillis < 0 || e.ElapsedMillis > 9_007_199_254_740_991 || !validLoopbackEndpoint(e.Endpoint) {
		return errors.New("invalid OpenCode transport failure evidence")
	}
	if e.ContextError != "" && e.ContextError != context.Canceled.Error() && e.ContextError != context.DeadlineExceeded.Error() {
		return errors.New("invalid OpenCode transport context evidence")
	}
	if e.DispatchState != TransportDispatchStateUnknown && e.DispatchState != TransportDispatchStateNotDispatched {
		return errors.New("invalid OpenCode transport dispatch state")
	}
	switch e.Phase {
	case TransportFailurePhaseMessagePostWait:
		if e.HTTPMethod != http.MethodPost || !locator(e.SessionID) || !locator(e.RequestMessageID) {
			return errors.New("invalid OpenCode message transport evidence")
		}
	case TransportFailurePhaseReadbackWait:
		if e.HTTPMethod != http.MethodGet && e.HTTPMethod != http.MethodPost && e.HTTPMethod != http.MethodDelete {
			return errors.New("invalid OpenCode readback transport evidence")
		}
		if e.SessionID != "" && !locator(e.SessionID) || e.RequestMessageID != "" {
			return errors.New("invalid OpenCode readback transport identity")
		}
	default:
		return errors.New("unknown OpenCode transport phase")
	}
	return nil
}

// TransportFailureFromError extracts validated transport evidence without
// exposing credentials or changing the stable public error text.
func TransportFailureFromError(err error) (TransportFailureEvidence, bool) {
	var carrier interface {
		transportFailureEvidence() TransportFailureEvidence
	}
	if !errors.As(err, &carrier) {
		return TransportFailureEvidence{}, false
	}
	evidence := carrier.transportFailureEvidence()
	if evidence.Validate() != nil {
		return TransportFailureEvidence{}, false
	}
	return evidence, true
}

type readbackFailure struct {
	message  string
	class    string
	cause    error
	evidence *TransportFailureEvidence
}

// Error returns the stable public readback failure without transport details.
func (e readbackFailure) Error() string { return e.message }

// Unwrap retains the actual transport cause for errors.Is/errors.As.
func (e readbackFailure) Unwrap() error { return e.cause }

func (e readbackFailure) readinessClass() string { return e.class }

func (e readbackFailure) transportFailureEvidence() TransportFailureEvidence {
	if e.evidence == nil {
		return TransportFailureEvidence{}
	}
	return *e.evidence
}

func requestTransportIdentity(method, path string, body []byte) (phase, sessionID, requestMessageID string) {
	phase = TransportFailurePhaseReadbackWait
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "session" && locator(parts[1]) {
		sessionID = parts[1]
	}
	if method != http.MethodPost || len(parts) != 3 || parts[0] != "session" || parts[2] != "message" || !locator(sessionID) {
		return phase, sessionID, ""
	}
	phase = TransportFailurePhaseMessagePostWait
	normal, err := canonical.Normalize(body)
	if err != nil {
		return phase, sessionID, ""
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(normal, &object) != nil {
		return phase, sessionID, ""
	}
	var id string
	if raw, ok := object["messageID"]; !ok || json.Unmarshal(raw, &id) != nil || !locator(id) {
		return phase, sessionID, ""
	}
	return phase, sessionID, id
}

func safeTransportError(err error, user, password string) string {
	text := "transport error"
	if err != nil && err.Error() != "" {
		text = err.Error()
	}
	for _, secret := range []string{password, user} {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	var safe strings.Builder
	for _, r := range text {
		if unicode.IsControl(r) {
			r = ' '
		}
		if safe.Len()+utf8.RuneLen(r) > maxTransportErrorBytes {
			break
		}
		safe.WriteRune(r)
	}
	text = strings.TrimSpace(safe.String())
	if text == "" {
		return "transport error"
	}
	return text
}

func safeTransportEvidenceText(text string, maximum int) bool {
	if text == "" || len(text) > maximum || !utf8.ValidString(text) {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
