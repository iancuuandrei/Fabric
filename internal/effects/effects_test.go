package effects

import (
	"strings"
	"testing"
)

func TestLostConfirmationAndExactApproval(t *testing.T) {
	h := strings.Repeat("a", 64)
	i := Intent{Version: 1, RunID: h, PlanID: h, RepositoryID: h, Kind: "filesystem", InputHash: h}
	id, err := i.ID()
	if err != nil {
		t.Fatal(err)
	}
	approval := Authorization{IntentID: id, Actor: "operator"}
	if err := approval.Validate(i); err != nil {
		t.Fatal(err)
	}
	if outcome, err := Outcome(i, nil); err != nil || outcome != "UNKNOWN" {
		t.Fatal("missing receipt inferred success", outcome, err)
	}
	changed := i
	changed.InputHash = strings.Repeat("b", 64)
	if err := approval.Validate(changed); err == nil {
		t.Fatal("approval survived input change")
	}
	r := Receipt{Version: 1, IntentID: id, Outcome: "CONFIRMED", ObservationHash: h}
	if _, err := Outcome(changed, &r); err == nil {
		t.Fatal("receipt applied to another intent")
	}
	r.ObservationHash = ""
	if _, err := Outcome(i, &r); err == nil {
		t.Fatal("confirmation without observation binding")
	}
}
