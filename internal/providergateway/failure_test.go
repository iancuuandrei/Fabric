package providergateway

import (
	"reflect"
	"strings"
	"sync"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/journal"
)

func TestFailureObservationPreservesPendingReservationAndIdempotency(t *testing.T) {
	f := newGatewayFixture(t, 100)
	call := f.begin(t, strings.Repeat("c", 64), 40)
	failure := FailureObservation{Version: 1, BindingID: call.BindingID, InvocationID: call.InvocationID, CallID: call.CallID, Class: FailureRateLimit, Phase: "http", HTTPStatus: 429}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := RecordFailure(f.path, f.binding, call, failure); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	state, err := Inspect(f.path)
	if err != nil || state.Pending == nil || !reflect.DeepEqual(*state.Pending, call) || len(state.Calls) != 1 || state.Calls[0].Receipt != nil || state.Calls[0].Failure == nil || !reflect.DeepEqual(*state.Calls[0].Failure, failure) || state.Finished || state.Exhausted {
		t.Fatal("failure changed pending effect state", err, state)
	}
	if err := access.RequireActive(f.accessPath, f.policy, f.intent); err != nil {
		t.Fatal("failure released admission", err)
	}
	if _, err := Begin(f.path, f.accessPath, f.policy, f.intent, f.binding, strings.Repeat("d", 64), 256, 40); err == nil {
		t.Fatal("failure authorized another call")
	}
	events, err := journal.Read(f.path)
	if err != nil || len(events) != 3 || events[2].Kind != failureEvent {
		t.Fatal("failure retry appended duplicate event", len(events), err)
	}
	before := events[len(events)-1].Hash
	changed := failure
	changed.Class = FailureQuota
	if RecordFailure(f.path, f.binding, call, changed) == nil {
		t.Fatal("changed observation accepted")
	}
	state.Calls[0].Failure.Class = FailureUnknown
	reloaded, err := Inspect(f.path)
	if err != nil || reloaded.Calls[0].Failure.Class != FailureRateLimit {
		t.Fatal("caller mutation changed journal", err)
	}
	after, err := journal.Read(f.path)
	if err != nil || len(after) != len(events) || after[len(after)-1].Hash != before {
		t.Fatal("rejected observation changed history", err)
	}
}

func TestFailureObservationRejectsForeignAndUnboundedEvidence(t *testing.T) {
	f := newGatewayFixture(t, 100)
	call := f.begin(t, strings.Repeat("c", 64), 40)
	base := FailureObservation{Version: 1, BindingID: call.BindingID, InvocationID: call.InvocationID, CallID: call.CallID, Class: FailureUnknown, Phase: "transport"}
	cases := map[string]func(*FailureObservation){
		"foreign call":                       func(v *FailureObservation) { v.CallID = strings.Repeat("d", 64) },
		"foreign expectation":                func(v *FailureObservation) { v.RequestExpectationID = strings.Repeat("d", 64) },
		"free text class":                    func(v *FailureObservation) { v.Class = "provider-secret-error" },
		"free text phase":                    func(v *FailureObservation) { v.Phase = "private-provider-message" },
		"fabricated HTTP":                    func(v *FailureObservation) { v.HTTPStatus = 429 },
		"provider category without response": func(v *FailureObservation) { v.Class = FailureAuthentication },
		"oversize body": func(v *FailureObservation) {
			v.Phase = "http"
			v.HTTPStatus = 500
			v.ResponseSHA256 = strings.Repeat("e", 64)
			v.ResponseBytes = f.model.MaxResponseBytes + 1
		},
		"size without hash":     func(v *FailureObservation) { v.Phase = "http"; v.HTTPStatus = 500; v.ResponseBytes = 1 },
		"success as HTTP error": func(v *FailureObservation) { v.Phase = "http"; v.HTTPStatus = 200 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			v := base
			mutate(&v)
			if RecordFailure(f.path, f.binding, call, v) == nil {
				t.Fatal("invalid observation accepted")
			}
		})
	}
	state, err := Inspect(f.path)
	if err != nil || state.Calls[0].Failure != nil || state.Pending == nil {
		t.Fatal("rejected failures changed state", err)
	}
	if err := RecordFailure(f.path, f.binding, call, base); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(f.path, failureEvent, base, func(events []journal.Event) error { _, err := replay(events); return err }); err == nil {
		t.Fatal("raw duplicate failure event accepted")
	}
}

func TestFailureEvidenceReferenceIsBoundToCallAndLegacyCompatible(t *testing.T) {
	f := newGatewayFixture(t, 100)
	call := f.begin(t, strings.Repeat("c", 64), 40)
	v := FailureObservation{Version: 1, BindingID: call.BindingID, InvocationID: call.InvocationID, CallID: call.CallID, Class: FailureMalformedResponse, Phase: "decode", HTTPStatus: 200}
	if err := v.validate(f.binding, call); err != nil {
		t.Fatal("legacy rejected", err)
	}
	for _, name := range []string{"../escape", strings.Repeat("d", 64) + ".failure.json", call.CallID + "/failure.json"} {
		v.Evidence = &FailureEvidenceRef{Artifact: name, SHA256: strings.Repeat("e", 64)}
		if v.validate(f.binding, call) == nil {
			t.Fatal("foreign evidence accepted", name)
		}
	}
	v.Evidence = &FailureEvidenceRef{Artifact: call.CallID + ".failure.json", SHA256: strings.Repeat("e", 64)}
	if err := RecordFailure(f.path, f.binding, call, v); err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(f.path)
	if err != nil || state.Calls[0].Failure.Evidence == nil || *state.Calls[0].Failure.Evidence != *v.Evidence || state.Pending == nil || state.Calls[0].Receipt != nil {
		t.Fatal("evidence changed settlement", err)
	}
}
