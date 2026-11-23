package evidencevalue

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

func feedbackFixture() (FeedbackRequest, []ResourceObservation) {
	cost := Numeric("200")
	model := Request{Version: 1, Resources: []Resource{{Name: "uncached_input_tokens", Limit: 1000}, {Name: "money_minor_units", Limit: 1000}}}
	return FeedbackRequest{Model: EncodeRequest(model), Allocations: map[string]Numeric{"uncached_input_tokens": "100", "money_minor_units": "100"}, Consumed: map[string][]string{}}, []ResourceObservation{{InvocationID: strings.Repeat("a", 64), Costs: map[string]*Numeric{"uncached_input_tokens": &cost}}}
}

func TestFeedbackUpdatesPricesOncePerKnownAxis(t *testing.T) {
	request, observations := feedbackFixture()
	before, _ := canonical.Bytes(request)
	result, err := ApplyFeedback(request, observations)
	if err != nil {
		t.Fatal(err)
	}
	model, _ := result.Request.Model.Decode()
	if model.Resources[0].Price != 1 || model.Resources[0].Used != 200 || model.Resources[0].GradientSquares != 1 || model.Resources[0].Limit != 1000 {
		t.Fatal("measured update changed budget or missed feedback", model.Resources)
	}
	if model.Resources[1].Used != 0 || len(result.Request.Consumed["money_minor_units"]) != 0 || !reflect.DeepEqual(result.Skipped, []string{"money_minor_units"}) {
		t.Fatal("unknown money became observed consumption", result)
	}
	after, _ := canonical.Bytes(request)
	if string(before) != string(after) {
		t.Fatal("feedback mutated caller")
	}
	repeated, err := ApplyFeedback(result.Request, observations)
	if err != nil || !reflect.DeepEqual(result.Request, repeated.Request) || len(repeated.Applied["uncached_input_tokens"]) != 0 {
		t.Fatal("receipt was priced twice", err)
	}
	// The unknown axis can later receive a real observation independently.
	money := Numeric("50")
	observations[0].Costs["money_minor_units"] = &money
	later, err := ApplyFeedback(result.Request, observations)
	if err != nil || len(later.Applied["money_minor_units"]) != 1 || len(later.Applied["uncached_input_tokens"]) != 0 {
		t.Fatal("axis cursor pooled known and unknown costs", err)
	}
	next, _ := later.Request.Model.Decode()
	if next.Resources[1].Used != 50 {
		t.Fatal("later known cost not retained")
	}
}

func TestFeedbackRejectsUnknownCursorsAndInvalidAllocations(t *testing.T) {
	for _, change := range []func(*FeedbackRequest){
		func(r *FeedbackRequest) { r.Consumed["uncached_input_tokens"] = []string{strings.Repeat("b", 64)} },
		func(r *FeedbackRequest) {
			r.Consumed["uncached_input_tokens"] = []string{strings.Repeat("a", 64), strings.Repeat("a", 64)}
		},
		func(r *FeedbackRequest) { r.Consumed["money_minor_units"] = []string{strings.Repeat("a", 64)} },
		func(r *FeedbackRequest) { r.Allocations["uncached_input_tokens"] = "0" },
		func(r *FeedbackRequest) { r.Allocations["money_minor_units"] = "NaN" },
		func(r *FeedbackRequest) { r.Allocations["undeclared"] = "1" },
		func(r *FeedbackRequest) { delete(r.Allocations, "uncached_input_tokens") },
	} {
		request, observations := feedbackFixture()
		change(&request)
		if _, err := ApplyFeedback(request, observations); err == nil {
			t.Fatal("invalid cursor/allocation accepted")
		}
	}
	request, observations := feedbackFixture()
	if _, err := ApplyFeedback(request, append(observations, observations[0])); err == nil {
		t.Fatal("duplicate invocation observations accepted")
	}
	request.Model.Resources[0].Used = "1e12"
	if _, err := ApplyFeedback(request, observations); err == nil {
		t.Fatal("overflowing usage accepted")
	}
}

func TestFeedbackUnderuseLowersPriceWithoutResettingBudget(t *testing.T) {
	request, observations := feedbackFixture()
	request.Model.Resources[0].Price = "2"
	low := Numeric("50")
	observations[0].Costs["uncached_input_tokens"] = &low
	result, err := ApplyFeedback(request, observations)
	if err != nil {
		t.Fatal(err)
	}
	model, _ := result.Request.Model.Decode()
	if math.Abs(model.Resources[0].Price-1) > 1e-12 || model.Resources[0].Used != 50 {
		t.Fatal("underuse feedback incorrect", model.Resources[0])
	}
}

func TestFeedbackPricesChangeFiniteAcquisitionSelection(t *testing.T) {
	model := fixture()
	model.Resources = []Resource{{Name: "uncached_input_tokens", Limit: 1000}}
	forecast := 500.0
	model.Actions[0].Costs = map[string]*float64{"uncached_input_tokens": &forecast}
	initial, err := Evaluate(model)
	if err != nil || initial.Selected == "" {
		t.Fatal("initial zero-price frontier should be positive", err)
	}
	request := FeedbackRequest{Model: EncodeRequest(model), Allocations: map[string]Numeric{"uncached_input_tokens": "10"}, Consumed: map[string][]string{}}
	observed := Numeric("100")
	feedback, err := ApplyFeedback(request, []ResourceObservation{{InvocationID: strings.Repeat("a", 64), Costs: map[string]*Numeric{"uncached_input_tokens": &observed}}})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := feedback.Request.Model.Decode()
	if err != nil {
		t.Fatal(err)
	}
	next, err := Evaluate(updated)
	if err != nil || next.Selected != "" || next.Estimates[0].Status != "resource_eligible" || next.Estimates[0].JEV == nil || *next.Estimates[0].JEV >= 0 {
		t.Fatal("observed price failed to change eligible acquisition decision", next, err)
	}
}
