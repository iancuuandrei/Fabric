package providergateway

import (
	"errors"
	"reflect"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
)

const failureEvent = "provider.call-failure"

// FailureClass is a finite observation category, never permission to retry.
type FailureClass string

// Failure categories distinguish observations without changing effect authority.
const (
	FailureRateLimit            FailureClass = "RATE_LIMIT"
	FailureTransientProvider    FailureClass = "TRANSIENT_PROVIDER"
	FailureTimeout              FailureClass = "TIMEOUT"
	FailureAuthentication       FailureClass = "AUTH"
	FailureInvalidRequest       FailureClass = "INVALID_REQUEST"
	FailureCapability           FailureClass = "CAPABILITY_UNAVAILABLE"
	FailureContentPolicy        FailureClass = "CONTENT_POLICY"
	FailureQuota                FailureClass = "QUOTA"
	FailureLocalCancellation    FailureClass = "LOCAL_CANCELLATION"
	FailureProviderCancellation FailureClass = "PROVIDER_CANCELLATION"
	FailureMalformedResponse    FailureClass = "MALFORMED_RESPONSE"
	FailureUnknown              FailureClass = "UNKNOWN_OUTCOME"
)

// FailureObservation retains bounded non-content evidence after an admitted
// attempt. Even a classified HTTP rejection leaves usage and effect settlement
// unknown; this record does not release reservations or permit another POST.
// FailureEvidenceRef pins private diagnostic bytes; Artifact is a local basename, never a provider locator.
type FailureEvidenceRef struct {
	Artifact string `json:"artifact"`
	SHA256   string `json:"sha256"`
}

// FailureObservation retains the bounded failure evidence for one admitted call.
type FailureObservation struct {
	Evidence             *FailureEvidenceRef `json:"evidence,omitempty"`
	Version              int                 `json:"version"`
	BindingID            string              `json:"binding_id"`
	InvocationID         string              `json:"invocation_id"`
	CallID               string              `json:"call_id"`
	RequestExpectationID string              `json:"request_expectation_id,omitempty"`
	Class                FailureClass        `json:"class"`
	Phase                string              `json:"phase"`
	HTTPStatus           int                 `json:"http_status,omitempty"`
	ResponseSHA256       string              `json:"response_sha256,omitempty"`
	ResponseBytes        int64               `json:"response_bytes,omitempty"`
}

func (f FailureObservation) validate(binding Binding, call CallIntent) error {
	if f.Evidence != nil {
		if safepath.RequireDigest(f.Evidence.SHA256) != nil || f.Evidence.Artifact != call.CallID+".failure.json" || strings.ContainsAny(f.Evidence.Artifact, "/\\") {
			return errors.New("invalid private failure evidence reference")
		}
	}
	bindingID, err := binding.ID()
	if err != nil || f.Version != 1 || f.BindingID != bindingID || f.InvocationID != binding.AccessInvocationID || f.CallID != call.CallID || f.RequestExpectationID != call.RequestExpectationID {
		return errors.New("provider failure differs from admitted call")
	}
	switch f.Class {
	case FailureRateLimit, FailureTransientProvider, FailureTimeout, FailureAuthentication,
		FailureInvalidRequest, FailureCapability, FailureContentPolicy, FailureQuota,
		FailureLocalCancellation, FailureProviderCancellation, FailureMalformedResponse, FailureUnknown:
	default:
		return errors.New("unsupported provider failure class")
	}
	switch f.Phase {
	case "local", "transport":
		if f.HTTPStatus != 0 || f.ResponseSHA256 != "" || f.ResponseBytes != 0 {
			return errors.New("non-response failure contains response evidence")
		}
		if f.Class != FailureUnknown && f.Class != FailureTimeout && f.Class != FailureLocalCancellation {
			return errors.New("non-response failure claims provider classification")
		}
	case "http":
		if f.HTTPStatus < 100 || f.HTTPStatus > 599 || f.HTTPStatus == 200 || f.Class == FailureLocalCancellation || f.Class == FailureMalformedResponse || f.Class == FailureCapability {
			return errors.New("invalid provider HTTP failure")
		}
	case "decode":
		if f.HTTPStatus != 200 || f.Class != FailureMalformedResponse && f.Class != FailureContentPolicy && f.Class != FailureProviderCancellation && f.Class != FailureUnknown {
			return errors.New("invalid provider decode failure")
		}
	default:
		return errors.New("unsupported provider failure phase")
	}
	if f.ResponseSHA256 == "" {
		if f.ResponseBytes != 0 {
			return errors.New("provider failure size lacks digest")
		}
	} else if safepath.RequireDigest(f.ResponseSHA256) != nil || f.ResponseBytes < 0 || f.ResponseBytes > binding.Model.MaxResponseBytes {
		return errors.New("invalid provider failure response evidence")
	}
	return nil
}

var errFailureAlreadyRecorded = errors.New("provider failure already recorded")

// RecordFailure records one exact observation while leaving the call pending.
// Identical concurrent recovery is idempotent; changed observations reject.
// It performs no network operation and requires no new access admission.
func RecordFailure(path string, binding Binding, call CallIntent, failure FailureObservation) error {
	if err := failure.validate(binding, call); err != nil {
		return err
	}
	_, err := journal.Append(path, failureEvent, failure, func(events []journal.Event) error {
		if len(events) == 0 {
			return errors.New("provider failure lacks journal prefix")
		}
		before, err := replay(events[:len(events)-1])
		if err != nil {
			return err
		}
		if before.Binding == nil || !reflect.DeepEqual(*before.Binding, binding) || len(before.Calls) == 0 || !reflect.DeepEqual(before.Calls[len(before.Calls)-1].Intent, call) {
			return errors.New("provider failure journal identity changed")
		}
		if existing := before.Calls[len(before.Calls)-1].Failure; existing != nil {
			if reflect.DeepEqual(*existing, failure) {
				return errFailureAlreadyRecorded
			}
			return errors.New("provider failure observation changed")
		}
		_, err = replay(events)
		return err
	})
	if errors.Is(err, errFailureAlreadyRecorded) {
		return nil
	}
	return err
}

func replayFailure(state *State, payload []byte) error {
	if state.Binding == nil || state.Pending == nil || len(state.Calls) == 0 || state.Calls[len(state.Calls)-1].Failure != nil {
		return errors.New("provider failure without unobserved pending call")
	}
	var failure FailureObservation
	if canonical.Decode(payload, &failure) != nil {
		return errors.New("invalid provider failure event")
	}
	if err := failure.validate(*state.Binding, *state.Pending); err != nil {
		return err
	}
	state.Calls[len(state.Calls)-1].Failure = &failure
	return nil
}
