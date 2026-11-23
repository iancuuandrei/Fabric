package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

func TestCodexAutoCompactInvocationOptionIsOptionalAndContentBound(t *testing.T) {
	profile := Profile{Runtime: "codex-app-server", Provider: "openai", Model: "fixture", Effort: "high", Role: "writer"}
	legacy, err := NewInvocation(profile, "exact input")
	if err != nil {
		t.Fatal(err)
	}
	noOption, err := NewInvocationWithCodexAutoCompact(profile, "exact input", nil)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.ID != noOption.ID {
		t.Fatal("nil auto-compaction option changed the legacy invocation ID")
	}
	legacyJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	noOptionJSON, err := json.Marshal(noOption)
	if err != nil || !reflect.DeepEqual(legacyJSON, noOptionJSON) {
		t.Fatalf("nil option changed legacy invocation JSON: %s vs %s; %v", legacyJSON, noOptionJSON, err)
	}
	legacyID, err := canonical.Hash("harness.invocation.v1", struct {
		Version int     `json:"version"`
		Profile Profile `json:"profile"`
		Input   string  `json:"input"`
	}{1, profile, "exact input"})
	if err != nil || legacy.ID != legacyID {
		t.Fatalf("nil option changed legacy hash payload: %s, %v", legacy.ID, err)
	}

	option := &CodexAutoCompactOptions{Version: CodexAutoCompactVersion, TokenLimit: 64000}
	enabled, err := NewInvocationWithCodexAutoCompact(profile, "exact input", option)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.ID == legacy.ID || enabled.CodexAutoCompactVersion != option.Version || enabled.CodexAutoCompactTokenLimit != option.TokenLimit {
		t.Fatal("enabled option did not get a distinct immutable identity")
	}
	if err := enabled.Validate(); err != nil {
		t.Fatal(err)
	}
	independentlyBuilt, err := NewInvocationWithCodexAutoCompact(profile, "exact input", option)
	if err != nil || independentlyBuilt != enabled {
		t.Fatalf("equivalent invocations are not comparable by value: %+v, %v", independentlyBuilt, err)
	}
	var decoded Invocation
	if err := json.Unmarshal(enabledJSON(t, enabled), &decoded); err != nil || decoded != enabled {
		t.Fatalf("option-bound invocation did not round-trip exactly: %+v, %v", decoded, err)
	}
	changed := enabled
	changed.CodexAutoCompactTokenLimit = 64001
	if err := changed.Validate(); err == nil {
		t.Fatal("changed threshold retained the old invocation identity")
	}
}

func enabledJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCodexAutoCompactRejectsInvalidOrUnsupportedOptions(t *testing.T) {
	profile := Profile{Runtime: "codex-app-server", Provider: "openai", Model: "fixture", Effort: "high", Role: "writer"}
	for _, option := range []*CodexAutoCompactOptions{
		{Version: 0, TokenLimit: 1000},
		{Version: CodexAutoCompactVersion, TokenLimit: 0},
		{Version: CodexAutoCompactVersion, TokenLimit: -1},
		{Version: CodexAutoCompactVersion, TokenLimit: MaxCodexAutoCompactTokenLimit + 1},
	} {
		if _, err := NewInvocationWithCodexAutoCompact(profile, "input", option); err == nil {
			t.Fatalf("invalid option admitted: %+v", option)
		}
	}
	profile.Runtime = "opencode-http"
	if _, err := NewInvocationWithCodexAutoCompact(profile, "input", &CodexAutoCompactOptions{Version: CodexAutoCompactVersion, TokenLimit: 1000}); err == nil {
		t.Fatal("Codex-specific option admitted for another runtime")
	}
}

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
