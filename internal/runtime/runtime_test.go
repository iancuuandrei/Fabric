package runtime

import (
	"context"
	"testing"
)

func TestObservedRoutingNeverFallsBackToRequest(t *testing.T) {
	i, err := NewInvocation(Profile{Runtime: "fake", Provider: "deterministic", Model: "fixture", Effort: "low", Role: "planner"}, "test objective")
	if err != nil {
		t.Fatal(err)
	}
	f := &Fake{}
	r, err := f.Execute(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	if r.ObservedProvider == nil || r.ObservedEffort == nil {
		t.Fatal("fake omitted supported observation")
	}
	other := "substitution"
	bad := r
	bad.ObservedProvider = &other
	if ValidateResult(i, bad, true) == nil {
		t.Fatal("provider substitution admitted")
	}
	bad = r
	bad.ObservedEffort = &other
	if ValidateResult(i, bad, true) == nil {
		t.Fatal("effort substitution admitted")
	}
	r.ObservedProvider, r.ObservedEffort = nil, nil
	if err := ValidateResult(i, r, true); err != nil {
		t.Fatal(err)
	}
	if r.ObservedProvider != nil || r.ObservedEffort != nil {
		t.Fatal("invented observations")
	}
	if f.Capabilities().Cancellation {
		t.Fatal("fake advertised unsupported target cancellation")
	}
}
