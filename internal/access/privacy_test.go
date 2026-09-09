package access

import (
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
)

func TestDurableAccessRejectsPrivacyChangeOnResume(t *testing.T) {
	p := fixture()
	p.Version = 2
	p.Kind, p.CredentialRef, p.AuthMode = "subscription", "", "session"
	p.Privacy = &PrivacyPolicy{Version: 1, Training: "excluded", Retention: "zero"}
	profileID, err := p.ID()
	if err != nil {
		t.Fatal(err)
	}
	route := Route{Version: 1, Role: "planner", Runtime: p.Runtime, Provider: p.Provider, Model: "fixture", Effort: "none", AccessID: profileID, Permission: "read-only"}
	policy := Policy{Version: 1, RunID: strings.Repeat("a", 64), Class: Public, Limits: Limits{Tokens: 100, Concurrency: 1}, Routes: []Route{route}, Profiles: []Profile{p}}
	policyID, err := policy.ID()
	if err != nil {
		t.Fatal(err)
	}
	intent := Intent{Attempt: 1, PolicyID: policyID, InputHash: strings.Repeat("b", 64), Route: route, Reservation: Reservation{Tokens: 50, BillingMode: "subscription"}}
	intent.Reservation.InvocationID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "access.jsonl")
	if err := ReserveDurable(path, policy, intent); err != nil {
		t.Fatal(err)
	}
	if err := RequireActive(path, policy, intent); err != nil {
		t.Fatal(err)
	}
	policy.Profiles[0].Privacy = &PrivacyPolicy{Version: 1, Training: "allowed", Retention: "provider-defined"}
	if RequireActive(path, policy, intent) == nil {
		t.Fatal("resumed invocation accepted different privacy terms")
	}
}

func TestPrivacyProfilePreservesLegacyIdentity(t *testing.T) {
	p := fixture()
	legacy := struct {
		Version           int     `json:"version"`
		Name              string  `json:"name"`
		Kind              string  `json:"kind"`
		Runtime           string  `json:"runtime"`
		Provider          string  `json:"provider"`
		CredentialRef     string  `json:"credential_ref"`
		AuthMode          string  `json:"auth_mode"`
		RepositoryClasses []Class `json:"repository_classes"`
	}{p.Version, p.Name, p.Kind, p.Runtime, p.Provider, p.CredentialRef, p.AuthMode, p.RepositoryClasses}
	want, err := canonical.Hash("harness.access-profile.v1", legacy)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ID()
	if err != nil || got != want {
		t.Fatalf("legacy identity changed: %s %s %v", got, want, err)
	}
	p.Privacy = &PrivacyPolicy{Version: 1, Training: "unknown", Retention: "unknown"}
	if p.Validate() == nil {
		t.Fatal("legacy version accepted new semantics")
	}
}

func TestPrivacyTermsAreProfileBoundWithoutImplicitClassAllowance(t *testing.T) {
	p := fixture()
	p.Version = 2
	p.Privacy = &PrivacyPolicy{Version: 1, Training: "unknown", Retention: "unknown"}
	unknown, err := p.ID()
	if err != nil {
		t.Fatal(err)
	}
	p.Privacy = &PrivacyPolicy{Version: 1, Training: "excluded", Retention: "zero"}
	excluded, err := p.ID()
	if err != nil || unknown == excluded {
		t.Fatal("privacy change not bound", err)
	}
	if p.Allows(p.Runtime, p.Provider, Private) == nil {
		t.Fatal("privacy terms invented private class permission")
	}
	p.Privacy.Retention = "bounded"
	p.Privacy.RetentionDays = 30
	bounded, err := p.ID()
	if err != nil || bounded == excluded {
		t.Fatal("retention not bound", err)
	}
	p.Privacy.RetentionDays = 31
	changed, _ := p.ID()
	if changed == bounded {
		t.Fatal("retention duration not bound")
	}
}

func TestPrivacyRejectsAmbiguousDeclarations(t *testing.T) {
	for _, policy := range []PrivacyPolicy{
		{},
		{Version: 1, Training: "excluded", Retention: "bounded"},
		{Version: 1, Training: "excluded", Retention: "zero", RetentionDays: 30},
		{Version: 1, Training: "excluded", Retention: "unknown", RetentionDays: -1},
		{Version: 1, Training: "maybe", Retention: "zero"},
		{Version: 1, Training: "unknown", Retention: "unknown", TermsSHA256: "not-evidence"},
	} {
		if policy.Validate() == nil {
			t.Fatalf("invalid privacy accepted: %+v", policy)
		}
	}
	p := fixture()
	p.Version = 2
	if p.Validate() == nil {
		t.Fatal("v2 omitted explicit terms")
	}
}
