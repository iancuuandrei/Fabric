package providertransport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/providergateway"
)

const maximumErrorBodyBytes = 16 << 10

// AttemptError is a sanitized observation of an admitted unresolved attempt.
// Recorded says whether its exact diagnostic reached the gateway journal. It
// always unwraps to ErrPending and grants no retry or budget-release authority.
type AttemptError struct {
	Observation providergateway.FailureObservation
	Recorded    bool
}

// Error contains only a finite class, never a provider message or credential.
func (e *AttemptError) Error() string {
	if e == nil {
		return "provider attempt unresolved"
	}
	switch e.Observation.Class {
	case providergateway.FailureRateLimit, providergateway.FailureTransientProvider,
		providergateway.FailureTimeout, providergateway.FailureAuthentication,
		providergateway.FailureInvalidRequest, providergateway.FailureCapability,
		providergateway.FailureContentPolicy, providergateway.FailureQuota,
		providergateway.FailureLocalCancellation, providergateway.FailureProviderCancellation,
		providergateway.FailureMalformedResponse, providergateway.FailureUnknown:
		return "provider attempt unresolved: " + string(e.Observation.Class)
	default:
		return "provider attempt unresolved"
	}
}

// Unwrap preserves conservative pending-call handling for existing callers.
func (e *AttemptError) Unwrap() error { return ErrPending }

type observedAttemptError struct {
	observation providergateway.FailureObservation
}

// Error deliberately excludes all upstream response data.
func (e *observedAttemptError) Error() string { return "provider response rejected" }

func recordAttemptFailure(ctx context.Context, request Request, call providergateway.CallIntent, cause error) error {
	failure := providergateway.FailureObservation{Class: providergateway.FailureUnknown, Phase: "transport"}
	var observed *observedAttemptError
	if errors.As(cause, &observed) && observed != nil {
		failure = observed.observation
	} else if errors.Is(cause, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		failure.Class, failure.Phase = providergateway.FailureTimeout, "local"
	} else if errors.Is(cause, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		failure.Class, failure.Phase = providergateway.FailureLocalCancellation, "local"
	} else {
		var timeout net.Error
		if errors.As(cause, &timeout) && timeout.Timeout() {
			failure.Class = providergateway.FailureTimeout
		}
	}
	failure.Version, failure.BindingID, failure.InvocationID, failure.CallID = 1, call.BindingID, call.InvocationID, call.CallID
	failure.RequestExpectationID = call.RequestExpectationID
	recordErr := providergateway.RecordFailure(request.GatewayJournalPath, request.Binding, call, failure)
	return errors.Join(pending(ctx), &AttemptError{Observation: failure, Recorded: recordErr == nil})
}

func observedTransportFailure(cause error) *observedAttemptError {
	failure := providergateway.FailureObservation{Class: providergateway.FailureUnknown, Phase: "transport"}
	var timeout net.Error
	switch {
	case errors.Is(cause, context.Canceled):
		failure.Class = providergateway.FailureLocalCancellation
	case errors.Is(cause, context.DeadlineExceeded):
		failure.Class = providergateway.FailureTimeout
	case errors.As(cause, &timeout) && timeout.Timeout():
		failure.Class = providergateway.FailureTimeout
	}
	return &observedAttemptError{observation: failure}
}

func observedDecodeFailure(raw []byte) *observedAttemptError {
	failure := providergateway.FailureObservation{Class: providergateway.FailureMalformedResponse, Phase: "decode", HTTPStatus: http.StatusOK}
	attachFailureDigest(&failure, raw)
	return &observedAttemptError{observation: failure}
}

func observedHTTPFailure(adapter string, status int, body []byte) *observedAttemptError {
	class := providergateway.FailureUnknown
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		class = providergateway.FailureAuthentication
	case http.StatusTooManyRequests:
		class = providergateway.FailureRateLimit
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		class = providergateway.FailureTimeout
	case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		class = providergateway.FailureInvalidRequest
	default:
		if status >= 500 && status <= 599 {
			class = providergateway.FailureTransientProvider
		}
	}
	// Only a protocol-specific machine discriminator refines an ambiguous status.
	// In particular, never classify spend limits by searching provider messages.
	if adapter == providergateway.OpenAIResponsesAdapter || adapter == providergateway.OpenAIChatCompletionsAdapter {
		code, kind := providerErrorDiscriminators(body)
		if status == http.StatusTooManyRequests && (code == "insufficient_quota" || kind == "insufficient_quota") {
			class = providergateway.FailureQuota
		}
	}
	failure := providergateway.FailureObservation{Class: class, Phase: "http", HTTPStatus: status}
	if len(body) <= maximumErrorBodyBytes {
		attachFailureDigest(&failure, body)
	}
	return &observedAttemptError{observation: failure}
}

func attachFailureDigest(failure *providergateway.FailureObservation, body []byte) {
	if body == nil {
		return
	}
	digest := sha256.Sum256(body)
	failure.ResponseSHA256, failure.ResponseBytes = hex.EncodeToString(digest[:]), int64(len(body))
}

func providerErrorDiscriminators(body []byte) (string, string) {
	if len(body) == 0 || len(body) > maximumErrorBodyBytes {
		return "", ""
	}
	normal, err := canonical.Normalize(body)
	if err != nil {
		return "", ""
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(normal, &envelope) != nil {
		return "", ""
	}
	var detail map[string]json.RawMessage
	if json.Unmarshal(envelope["error"], &detail) != nil {
		return "", ""
	}
	var code, kind string
	if json.Unmarshal(detail["code"], &code) != nil {
		code = ""
	}
	if json.Unmarshal(detail["type"], &kind) != nil {
		kind = ""
	}
	return code, kind
}

// attachDecodeEvidence never exposes raw diagnostic text through the public error.
func attachDecodeEvidence(f *observedAttemptError, evidence *responseEvidence, code, stage string, cause error) error {
	if evidence == nil {
		return errors.New("response evidence missing")
	}
	ref, err := evidence.recordFailure(code, stage, cause)
	if err != nil {
		return err
	}
	f.observation.Evidence = ref
	return nil
}
