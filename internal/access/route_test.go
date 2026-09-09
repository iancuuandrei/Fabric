package access

import "testing"

func TestExactRouteAdmission(t *testing.T) {
	p := fixture()
	id, _ := p.ID()
	r := Route{Version: 1, Role: "writer", Runtime: p.Runtime, Provider: p.Provider, Model: "vendor/model:revision", Effort: "low", AccessID: id, Permission: "workspace-write"}
	if err := AdmitRoute(r, r, p, Public); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Route){
		func(r *Route) { r.Model = "other" },
		func(r *Route) { r.Provider = "other" },
		func(r *Route) { r.Runtime = "other" },
		func(r *Route) { r.Role = "fixer" },
		func(r *Route) { r.Effort = "high" },
		func(r *Route) { r.Permission = "read-only" },
		func(r *Route) { r.AccessID = "" },
	} {
		requested := r
		change(&requested)
		if AdmitRoute(r, requested, p, Public) == nil {
			t.Fatal("route substitution admitted")
		}
	}
	if AdmitRoute(r, r, p, Private) == nil {
		t.Fatal("privacy violation admitted")
	}
	changed := p
	changed.CredentialRef = "OTHER_KEY"
	if AdmitRoute(r, r, changed, Public) == nil {
		t.Fatal("access substitution admitted")
	}
	if AdmitRoute(r, r, Profile{}, Public) == nil {
		t.Fatal("missing profile admitted")
	}
	for _, role := range []string{"planner", "explorer", "reviewer"} {
		read := r
		read.Role = role
		if read.Validate() == nil {
			t.Fatal("read-only role acquired write capability")
		}
		read.Permission = "read-only"
		if err := AdmitRoute(read, read, p, Public); err != nil {
			t.Fatal(err)
		}
	}
}
