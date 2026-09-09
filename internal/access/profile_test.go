package access

import "testing"

func fixture() Profile {
	return Profile{Version: 1, Name: "public-api", Kind: "api", Runtime: "responses", Provider: "fixture", CredentialRef: "FIXTURE_KEY", RepositoryClasses: []Class{Public}}
}

func TestPolicyDenials(t *testing.T) {
	p := fixture()
	for _, change := range []func(*Profile){
		func(p *Profile) { p.Version = 0 },
		func(p *Profile) { p.Kind = "free" },
		func(p *Profile) { p.CredentialRef = "" },
		func(p *Profile) { p.CredentialRef = "secret\nvalue" },
		func(p *Profile) { p.AuthMode = "session" },
		func(p *Profile) { p.RepositoryClasses = nil },
		func(p *Profile) { p.RepositoryClasses = []Class{Public, Public} },
		func(p *Profile) { p.RepositoryClasses = []Class{"INTERNAL"} },
	} {
		bad := p
		change(&bad)
		if _, err := bad.ID(); err == nil {
			t.Fatal("invalid policy hashed")
		}
	}
	for _, route := range []struct {
		runtime, provider string
		class             Class
	}{
		{"other", "fixture", Public}, {"responses", "other", Public},
		{"responses", "fixture", Private}, {"responses", "fixture", Confidential},
		{"responses", "fixture", ""},
	} {
		if p.Allows(route.runtime, route.provider, route.class) == nil {
			t.Fatal("disallowed access admitted")
		}
	}
	if err := p.Allows("responses", "fixture", Public); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityAndSubscription(t *testing.T) {
	p := fixture()
	p.RepositoryClasses = []Class{Private, Public}
	id, err := p.ID()
	if err != nil || p.RepositoryClasses[0] != Private {
		t.Fatal("identity mutated policy", err)
	}
	q := p
	q.RepositoryClasses = []Class{Public, Private}
	other, _ := q.ID()
	if id != other {
		t.Fatal("set order changed identity")
	}
	q.Kind, q.CredentialRef, q.AuthMode = "subscription", "", "runtime-session"
	other, err = q.ID()
	if err != nil || id == other {
		t.Fatal("billing mode not bound", err)
	}
	if err := q.Allows("responses", "fixture", Private); err != nil {
		t.Fatal(err)
	}
	q.AuthMode = ""
	if q.Validate() == nil {
		t.Fatal("implicit session admitted")
	}
	q.CredentialRef = "OPENCODE_GO_KEY"
	keyID, err := q.ID()
	if err != nil || keyID == other {
		t.Fatal("subscription key authentication not independently bound", err)
	}
	if q.Kind != "subscription" {
		t.Fatal("key authentication changed billing mode")
	}
	q.AuthMode = "session"
	if q.Validate() == nil {
		t.Fatal("ambiguous subscription authentication admitted")
	}
}
