package worktree

import (
	"errors"
	"os"
	"strings"
	"testing"

	"harness.local/engorch/internal/effects"
)

func TestLeaseWithOwnershipGuardsExactRequest(t *testing.T) {
	_, request := fixture(t)
	lease, err := Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	called := false
	if err := lease.WithOwnership(request, func(identity LeaseIdentity) error {
		called = true
		if identity.Request != request {
			t.Fatal("lease identity changed request")
		}
		if _, err := identity.ID(); err != nil {
			t.Fatal(err)
		}
		return nil
	}); err != nil || !called {
		t.Fatal("exact live lease rejected", err)
	}

	_, foreign := fixture(t)
	if err := lease.WithOwnership(foreign, func(LeaseIdentity) error { return nil }); err == nil {
		t.Fatal("foreign workspace accepted")
	}
	substitutedRequest := request
	substitutedRequest.Source.Commit = strings.Repeat("b", len(request.Source.Commit))
	if err := substitutedRequest.Validate(); err != nil {
		t.Fatal("invalid substitution fixture", err)
	}
	if err := lease.WithOwnership(substitutedRequest, func(LeaseIdentity) error { return nil }); err == nil {
		t.Fatal("same lock path with substituted immutable request accepted")
	}
	want := errors.New("visit failure")
	if err := lease.WithOwnership(request, func(LeaseIdentity) error { return want }); !errors.Is(err, want) {
		t.Fatal("visit error not preserved", err)
	}
}

func TestAdoptedLeaseWithOwnershipRetainsRecoveryRequest(t *testing.T) {
	_, request := fixture(t)
	owner, err := Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareLeaseRecovery(request, "attempt", "fixture owner exited", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.guard.Close(); err != nil {
		t.Fatal(err)
	}
	owner.file = nil
	owner.guard = nil
	planID, err := plan.ID()
	if err != nil {
		t.Fatal(err)
	}
	repositoryID, err := request.Source.ID()
	if err != nil {
		t.Fatal(err)
	}
	intent := effects.Intent{Version: 1, RunID: request.RunID, PlanID: strings.Repeat("c", 64), RepositoryID: repositoryID, Kind: "lease_recovery", InputHash: planID}
	intentID, err := intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	authorization := effects.Authorization{IntentID: intentID, Actor: "fixture"}
	adopted, err := AdoptLease(plan, intent, authorization)
	if err != nil {
		t.Fatal(err)
	}
	defer adopted.Close()
	if err := adopted.WithOwnership(request, func(identity LeaseIdentity) error {
		if identity.Request != request {
			t.Fatal("adopted lease identity changed request")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseWithOwnershipRejectsClosedAndSubstitutedLease(t *testing.T) {
	_, request := fixture(t)
	closed, err := Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := closed.WithOwnership(request, func(LeaseIdentity) error { return nil }); err == nil {
		t.Fatal("closed lease accepted")
	}

	substituted, err := Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	defer substituted.Close()
	if err := os.WriteFile(substituted.path, []byte("different owner"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := substituted.WithOwnership(request, func(LeaseIdentity) error { return nil }); err == nil {
		t.Fatal("substituted lease accepted")
	}
}

func TestLeaseWithOwnershipSerializesClose(t *testing.T) {
	_, request := fixture(t)
	lease, err := Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	guardDone := make(chan error, 1)
	go func() {
		guardDone <- lease.WithOwnership(request, func(LeaseIdentity) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- lease.Close() }()
	select {
	case err := <-closeDone:
		t.Fatal("Close returned while ownership callback was active", err)
	default:
	}
	close(release)
	if err := <-guardDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}
