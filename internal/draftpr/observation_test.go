package draftpr

import "testing"

func TestDraftObservationRequiresExactHostedState(t *testing.T) {
	plan := fixturePlan(t)
	request, err := plan.Request()
	if err != nil {
		t.Fatal(err)
	}
	good := Observation{Number: 7, URL: "https://github.com/fixture/project/pull/7", State: "open", Draft: true, Title: request.Title, Body: request.Body, Head: BranchObservation{plan.Repository, request.Head, plan.Push.Candidate.Head}, Base: BranchObservation{plan.Repository, request.Base, plan.BaseCommit}}
	if err := good.Validate(plan); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Observation){
		func(o *Observation) { o.Number = 0 },
		func(o *Observation) { o.URL = "https://other.example/pull/7" },
		func(o *Observation) { o.Draft = false },
		func(o *Observation) { o.State = "closed" },
		func(o *Observation) { o.Title += " changed" },
		func(o *Observation) { o.Body = plan.Body },
		func(o *Observation) { o.Head.Repository = "other/project" },
		func(o *Observation) { o.Head.Ref = "other" },
		func(o *Observation) { o.Head.Commit = plan.BaseCommit },
		func(o *Observation) { o.Base.Commit = plan.Push.Candidate.Head },
	} {
		bad := good
		mutate(&bad)
		if err := bad.Validate(plan); err == nil {
			t.Fatal("substituted hosted state admitted")
		}
	}
}
