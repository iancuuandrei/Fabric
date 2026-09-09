package config

import (
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
)

func TestOptionalPlannerContractIsStrictAndIdentityBound(t *testing.T) {
	legacy, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	legacyID, err := legacy.ID()
	if err != nil {
		t.Fatal(err)
	}
	configuredRaw := strings.Replace(Example, "\n[planner]", "\nplanner_contract = \"plan-v1\"\n\n[planner]", 1)
	configured, err := Parse([]byte(configuredRaw))
	if err != nil {
		t.Fatal(err)
	}
	if configured.PlannerContract != "plan-v1" {
		t.Fatalf("planner contract not decoded: %q", configured.PlannerContract)
	}
	configuredID, err := configured.ID()
	if err != nil || configuredID == legacyID {
		t.Fatalf("planner contract did not change configuration identity: %v", err)
	}
	for _, raw := range []string{
		strings.Replace(Example, "\n[planner]", "\nplanner_contract = \"plan-v2\"\n\n[planner]", 1),
		strings.Replace(Example, "\n[planner]", "\nplanner_contract = \"writer-v1\"\n\n[planner]", 1),
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatal("unsupported planner contract admitted")
		}
	}
	legacyJSON, err := canonical.Bytes(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacyJSON), "planner_contract") {
		t.Fatal("empty planner contract changed legacy serialization")
	}
}
