package providercredential

import (
	"context"
	"strings"
	"testing"
)

func TestEnvironmentSourceUsesOnlyExactExplicitMapping(t *testing.T) {
	mapping := map[string]string{"provider-key": "EXACT_PROVIDER_KEY"}
	calls := []string{}
	source, err := NewEnvironmentSource(mapping, func(name string) (string, bool) {
		calls = append(calls, name)
		if name != "EXACT_PROVIDER_KEY" {
			t.Fatal("looked up unselected environment name", name)
		}
		return "fixture-secret", true
	})
	if err != nil {
		t.Fatal(err)
	}
	mapping["provider-key"] = "MUTATED_KEY"
	mapping["other"] = "OTHER_KEY"
	secret, err := source.Take(context.Background(), "provider-key")
	if err != nil || string(secret) != "fixture-secret" || len(calls) != 1 || calls[0] != "EXACT_PROVIDER_KEY" {
		t.Fatal("exact mapped credential was not returned", err, calls)
	}
	clear(secret)
	if _, err := source.Take(context.Background(), "other"); err == nil || len(calls) != 1 {
		t.Fatal("unmapped reference triggered ambient lookup", calls)
	}
}

func TestEnvironmentSourceReturnsOwnedBytes(t *testing.T) {
	source, err := NewEnvironmentSource(map[string]string{"key": "KEY"}, func(string) (string, bool) { return "secret-value", true })
	if err != nil {
		t.Fatal(err)
	}
	first, err := source.Take(context.Background(), "key")
	if err != nil {
		t.Fatal(err)
	}
	first[0] = 'X'
	second, err := source.Take(context.Background(), "key")
	if err != nil || string(second) != "secret-value" {
		t.Fatal("returned credential storage was shared", err)
	}
	clear(first)
	clear(second)
}

func TestEnvironmentSourceRejectsInvalidMappings(t *testing.T) {
	tests := []map[string]string{
		nil,
		{"bad ref": "GOOD"},
		{"good": "lowercase"},
		{"good": "1PREFIX"},
		{"good": "HAS-DASH"},
	}
	for _, mapping := range tests {
		if _, err := NewEnvironmentSource(mapping, func(string) (string, bool) { return "secret", true }); err == nil {
			t.Fatal("invalid mapping accepted", mapping)
		}
	}
	if _, err := NewEnvironmentSource(map[string]string{"good": "GOOD"}, nil); err == nil {
		t.Fatal("nil lookup accepted")
	}
}

func TestEnvironmentSourceRejectsMissingAndInvalidSecretsWithoutLeak(t *testing.T) {
	for name, value := range map[string]string{
		"empty":      "",
		"space":      "secret value",
		"newline":    "secret\nvalue",
		"nul":        "secret\x00value",
		"over_bound": strings.Repeat("s", maxSecretBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			source, err := NewEnvironmentSource(map[string]string{"ref": "SECRET_ENV"}, func(string) (string, bool) { return value, true })
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.Take(context.Background(), "ref"); err == nil || value != "" && strings.Contains(err.Error(), value) || strings.Contains(err.Error(), "SECRET_ENV") || strings.Contains(err.Error(), "ref") {
				t.Fatal("invalid secret error leaked source details", err)
			}
		})
	}
	source, _ := NewEnvironmentSource(map[string]string{"ref": "SECRET_ENV"}, func(string) (string, bool) { return "sentinel-secret", false })
	if _, err := source.Take(context.Background(), "ref"); err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("missing secret was exposed", err)
	}
}

func TestEnvironmentSourceHonorsCancellationBeforeLookup(t *testing.T) {
	calls := 0
	source, err := NewEnvironmentSource(map[string]string{"ref": "SECRET_ENV"}, func(string) (string, bool) {
		calls++
		return "secret", true
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Take(ctx, "ref"); err == nil || calls != 0 {
		t.Fatal("cancelled resolution reached lookup", err, calls)
	}
	if _, err := source.Take(nil, "ref"); err == nil || calls != 0 {
		t.Fatal("nil context reached lookup", err, calls)
	}
}

func TestEnvironmentSourceRejectsOversizedStringAfterOneExplicitLookup(t *testing.T) {
	calls := 0
	source, err := NewEnvironmentSource(map[string]string{"ref": "SECRET_ENV"}, func(name string) (string, bool) {
		calls++
		if name != "SECRET_ENV" {
			t.Fatal("looked up unexpected environment name", name)
		}
		return strings.Repeat("s", maxSecretBytes+1), true
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Take(context.Background(), "ref"); err == nil || calls != 1 {
		t.Fatal("oversized environment value accepted or lookup repeated", err, calls)
	}
}
