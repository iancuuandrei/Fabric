package opencode

import (
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/runtime"
)

func TestInvocationAccessRejectsCrossRoleAndInput(t *testing.T) {
	i, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "low", Role: "reviewer"}, "review task")
	if err != nil {
		t.Fatal(err)
	}
	input, _ := access.InputID(i.Input)
	a := access.Intent{Attempt: 1, PolicyID: strings.Repeat("a", 64), InputHash: input, Route: access.Route{Version: 1, Role: "reviewer", Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "low", AccessID: strings.Repeat("b", 64), Permission: "read-only"}, Reservation: access.Reservation{Tokens: 100, BillingMode: "subscription"}}
	a.Reservation.InvocationID, err = a.ID()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateInvocationAccess(i, a); err != nil {
		t.Fatal(err)
	}
	foreign := a
	foreign.Route.Role = "explorer"
	foreign.Reservation.InvocationID, _ = foreign.ID()
	if err := ValidateInvocationAccess(i, foreign); err == nil {
		t.Fatal("cross-role reservation admitted")
	}
	foreign = a
	foreign.InputHash = strings.Repeat("c", 64)
	foreign.Reservation.InvocationID, _ = foreign.ID()
	if err := ValidateInvocationAccess(i, foreign); err == nil {
		t.Fatal("foreign input admitted")
	}
	foreign = a
	foreign.Reservation.Tokens++
	if err := ValidateInvocationAccess(i, foreign); err == nil {
		t.Fatal("changed reservation admitted")
	}
}

func TestDispatchBindsImmutableRuntimeRouting(t *testing.T) {
	i, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "low", Role: "reviewer"}, "review task")
	if err != nil {
		t.Fatal(err)
	}
	d, err := DispatchForInvocation(i, "ses_fixture", "msg_fixture", "review", "/candidate", "/candidate")
	if err != nil || d.Text != i.Input || d.Binding.Provider != i.Profile.Provider || d.Binding.Model != i.Profile.Model || d.Binding.Variant != i.Profile.Effort || d.Binding.Agent != "review" {
		t.Fatal("routing changed", err)
	}
	i.Profile.Model = "other"
	if _, err := DispatchForInvocation(i, "ses_fixture", "msg_fixture", "review", "/candidate", "/candidate"); err == nil {
		t.Fatal("mutated invocation accepted")
	}
}

func TestDurableInvocationAccessBindsEveryRoleToActiveIntent(t *testing.T) {
	for _, role := range []string{"planner", "explorer", "reviewer", "writer", "fixer"} {
		t.Run(role, func(t *testing.T) {
			permission := "read-only"
			if role == "writer" || role == "fixer" {
				permission = "workspace-write"
			}
			invocation, policy, admission := durableInvocationFixture(t, role, permission)
			path := filepath.Join(t.TempDir(), "access.jsonl")

			if err := ValidateDurableInvocationAccess(path, policy, invocation, admission); err == nil {
				t.Fatal("missing durable reservation accepted")
			}
			if err := access.ReserveDurable(path, policy, admission); err != nil {
				t.Fatal(err)
			}
			if err := ValidateDurableInvocationAccess(path, policy, invocation, admission); err != nil {
				t.Fatal("active bound reservation rejected:", err)
			}

			foreignInvocation, err := runtime.NewInvocation(invocation.Profile, invocation.Input+" changed")
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateDurableInvocationAccess(path, policy, foreignInvocation, admission); err == nil {
				t.Fatal("changed runtime input accepted")
			}

			changed := admission
			changed.Attempt++
			changed.Reservation.InvocationID, err = changed.ID()
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateDurableInvocationAccess(path, policy, invocation, changed); err == nil {
				t.Fatal("unrecorded changed intent accepted")
			}

			drifted := policy
			drifted.RunID = strings.Repeat("d", 64)
			if err := ValidateDurableInvocationAccess(path, drifted, invocation, admission); err == nil {
				t.Fatal("active reservation accepted under changed policy")
			}

			routeID, err := admission.Route.ID()
			if err != nil {
				t.Fatal(err)
			}
			if err := access.RecordTerminal(path, policy, access.Receipt{
				InvocationID: admission.Reservation.InvocationID, RouteID: routeID, Status: "failed",
			}); err != nil {
				t.Fatal(err)
			}
			if err := ValidateDurableInvocationAccess(path, policy, invocation, admission); err == nil {
				t.Fatal("terminal reservation accepted")
			}
		})
	}
}

func durableInvocationFixture(t *testing.T, role, permission string) (runtime.Invocation, access.Policy, access.Intent) {
	t.Helper()
	profile := access.Profile{
		Version: 1, Name: "opencode-session", Kind: "subscription",
		Runtime: "opencode-http", Provider: "fixture", AuthMode: "session",
		RepositoryClasses: []access.Class{access.Private},
	}
	profileID, err := profile.ID()
	if err != nil {
		t.Fatal(err)
	}
	route := access.Route{
		Version: 1, Role: role, Runtime: profile.Runtime, Provider: profile.Provider,
		Model: "model", Effort: "low", AccessID: profileID, Permission: permission,
	}
	policy := access.Policy{
		Version: 1, RunID: strings.Repeat("a", 64), Class: access.Private,
		Limits: access.Limits{Tokens: 1000, Concurrency: 1},
		Routes: []access.Route{route}, Profiles: []access.Profile{profile},
	}
	policyID, err := policy.ID()
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(runtime.Profile{
		Runtime: route.Runtime, Provider: route.Provider, Model: route.Model, Effort: route.Effort, Role: route.Role,
	}, "perform "+role+" task")
	if err != nil {
		t.Fatal(err)
	}
	inputHash, err := access.InputID(invocation.Input)
	if err != nil {
		t.Fatal(err)
	}
	admission := access.Intent{
		Attempt: 1, PolicyID: policyID, InputHash: inputHash, Route: route,
		Reservation: access.Reservation{Tokens: 100, BillingMode: "subscription"},
	}
	admission.Reservation.InvocationID, err = admission.ID()
	if err != nil {
		t.Fatal(err)
	}
	return invocation, policy, admission
}
