package worktree

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/effects"
)

func TestLeaseAdoptionRequiresQuiescenceTokenAndKernelExclusion(t *testing.T) {
	_, request := fixture(t)
	owner, err := Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := PrepareLeaseRecovery(request, "attempt", "worker exited", false); err == nil {
		t.Fatal("missing stopped attestation accepted")
	}
	plan, err := PrepareLeaseRecovery(request, "attempt", "worker exit observed by fixture", true)
	if err != nil {
		t.Fatal(err)
	}
	id, err := plan.ID()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := request.Source.ID()
	if err != nil {
		t.Fatal(err)
	}
	intent := effects.Intent{Version: 1, RunID: request.RunID, PlanID: strings.Repeat("b", 64), RepositoryID: repo, Kind: "lease_recovery", InputHash: id}
	intentID, err := intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	auth := effects.Authorization{IntentID: intentID, Actor: "fixture"}
	if _, err := AdoptLease(plan, intent, auth); err == nil {
		t.Fatal("active kernel owner displaced")
	}
	// Simulate an exited owner's closed handle while preserving the persistent file.
	if err := owner.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.guard.Close(); err != nil {
		t.Fatal(err)
	}
	owner.file = nil
	owner.guard = nil
	if err := os.WriteFile(owner.path, []byte(strings.Repeat("f", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := AdoptLease(plan, intent, auth); err == nil {
		t.Fatal("changed on-disk token adopted")
	}
	if err := os.WriteFile(owner.path, []byte(owner.token), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := AdoptLease(plan, intent, effects.Authorization{}); err == nil {
		t.Fatal("unapproved adoption accepted")
	}
	changed := plan
	changed.TokenSHA256 = strings.Repeat("0", 64)
	if _, err := AdoptLease(changed, intent, auth); err == nil {
		t.Fatal("substituted token plan accepted")
	}
	data, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), "recovery-intent.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	adopted, err := AdoptLease(plan, intent, auth)
	if err != nil {
		t.Fatal(err)
	}
	defer adopted.Close()
	if _, err := AdoptLease(plan, intent, auth); err == nil {
		t.Fatal("two recovery owners admitted")
	}
	if _, err := Acquire(request); err == nil {
		t.Fatal("ordinary acquisition bypassed adopted lease")
	}
	if err := adopted.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := AdoptLease(plan, intent, auth); err == nil {
		t.Fatal("removed token reused")
	}
	next, err := Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseCloseRejectsOversizedToken(t *testing.T) {
	_, request := fixture(t)
	lease, err := Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(lease.token + strings.Repeat("x", 4096))
	if err := os.WriteFile(lease.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err == nil {
		t.Fatal("oversized token accepted")
	}
	retained, err := os.ReadFile(lease.path)
	if err != nil || string(retained) != string(data) {
		t.Fatal("oversized replacement token removed or changed", err)
	}
}
