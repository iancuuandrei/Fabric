package config

import (
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
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
	repairContractRaw := strings.Replace(Example, "\n[planner]", "\nplanner_contract = \"plan-graph-v3\"\n\n[planner]", 1)
	repairContract, err := Parse([]byte(repairContractRaw))
	if err != nil || repairContract.PlannerContract != "plan-graph-v3" {
		t.Fatalf("repair graph planner contract rejected: %v", err)
	}
	parallelContractRaw := strings.Replace(Example, "\n[planner]", "\nwriter_contract = \"anchored-edits-v1\"\nplanner_contract = \"plan-graph-v4\"\nexplorer_contract = \"json-v2\"\n\n[planner]", 1)
	if _, err := Parse([]byte(parallelContractRaw)); err == nil {
		t.Fatal("parallel graph contract admitted without writer and explorer routes")
	}
	parallelFixture := parallelContractRaw + `

[writer]
runtime = "fake"
provider = "deterministic"
model = "fixture-v1"
effort = "none"
role = "writer"

[explorer]
runtime = "fake"
provider = "deterministic"
model = "fixture-v1"
effort = "none"
role = "explorer"
`
	if _, err := Parse([]byte(parallelFixture)); err != nil {
		t.Fatalf("parallel graph local fixture routes rejected: %v", err)
	}
	unsupportedRuntime := strings.Replace(parallelFixture, "runtime = \"fake\"\nprovider = \"deterministic\"\nmodel = \"fixture-v1\"\neffort = \"none\"\nrole = \"writer\"", "runtime = \"provider-api\"\nprovider = \"openai\"\nmodel = \"fixture-v1\"\neffort = \"none\"\nrole = \"writer\"", 1)
	if _, err := Parse([]byte(unsupportedRuntime)); err == nil {
		t.Fatal("parallel graph contract admitted an unqualified provider writer")
	}
	legacyJSON, err := canonical.Bytes(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacyJSON), "planner_contract") {
		t.Fatal("empty planner contract changed legacy serialization")
	}
}
