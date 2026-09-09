package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/draftpr"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitlocal"
	"harness.local/engorch/internal/gitpush"
)

type fixtureDraftHost struct {
	t            *testing.T
	path         string
	base         string
	creates      int
	available    bool
	loseResponse bool
}

func (h *fixtureDraftHost) ReadBranch(_ context.Context, repo, ref string) (draftpr.BranchObservation, error) {
	return draftpr.BranchObservation{Repository: repo, Ref: strings.TrimPrefix(ref, "refs/heads/"), Commit: h.base}, nil
}
func (h *fixtureDraftHost) Preflight(context.Context, draftpr.Plan) error { return nil }
func (h *fixtureDraftHost) Create(_ context.Context, p draftpr.Plan, i effects.Intent, a effects.Authorization) (draftpr.Observation, error) {
	s, err := Inspect(h.path)
	if err != nil || s.Draft == nil || s.Draft.Outcome != "UNKNOWN" || s.Draft.Intent.Prepared.Intent != i {
		h.t.Fatal("creation before durable intent", err)
	}
	if err := a.Validate(i); err != nil {
		h.t.Fatal(err)
	}
	h.creates++
	if h.loseResponse {
		return draftpr.Observation{}, errors.New("fixture lost response")
	}
	return h.Reconcile(context.Background(), p)
}
func (h *fixtureDraftHost) Reconcile(_ context.Context, p draftpr.Plan) (draftpr.Observation, error) {
	if !h.available {
		return draftpr.Observation{}, errors.New("fixture host unavailable")
	}
	r, err := p.Request()
	if err != nil {
		return draftpr.Observation{}, err
	}
	return draftpr.Observation{Number: 7, URL: "https://github.com/fixture/project/pull/7", State: "open", Draft: true, Title: r.Title, Body: r.Body, Head: draftpr.BranchObservation{Repository: p.Repository, Ref: r.Head, Commit: p.Push.Candidate.Head}, Base: draftpr.BranchObservation{Repository: p.Repository, Ref: r.Base, Commit: p.BaseCommit}}, nil
}

func TestDraftControllerWithSimulatedHost(t *testing.T) {
	ctx := context.Background()
	path, _ := approvedRepository(t, config.Check{Name: "git-version", Argv: []string{"git", "--version"}, TimeoutSeconds: 10})
	if _, err := StartWorkspace(ctx, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(ctx, path); err != nil {
		t.Fatal(err)
	}
	identity := gitlocal.Identity{Name: "Fixture", Email: "fixture@example.invalid", UnixSeconds: 1788739200}
	commit, err := PrepareCommit(ctx, path, identity, identity, "draft fixture\n")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := commit.Intent.ID()
	s, err := ExecuteCommit(ctx, path, commit, effects.Authorization{IntentID: id, Actor: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	// Only the prerequisite hosted push observation is synthetic. Worktree,
	// commit, append/replay and draft controller operations below are real.
	repoID, _ := s.Creation.Repository.ID()
	push := gitpush.Plan{Version: 1, Nonce: "fixture", RepositoryID: repoID, Workspace: *s.Workspace, Candidate: *s.Candidate, Destination: "https://github.com/fixture/project.git", TargetRef: "refs/heads/initial"}
	pi, err := push.Intent(s.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := pi.ID()
	if err := Append(path, "push.intent", PushIntent{Prepared: PreparedPush{Plan: push, Intent: pi}, Authorization: effects.Authorization{IntentID: pid, Actor: "fixture"}}); err != nil {
		t.Fatal(err)
	}
	head := push.Candidate.Head
	po := PushObservation{Remote: &gitpush.RefObservation{TargetRef: push.TargetRef, Commit: &head}, Candidate: &push.Candidate}
	hash, _ := canonical.Hash("harness.push-observation.v1", po)
	if err := Append(path, "push.observed", PushReceipt{Receipt: effects.Receipt{Version: 1, IntentID: pid, Outcome: "CONFIRMED", ObservationHash: hash}, Observation: po}); err != nil {
		t.Fatal(err)
	}
	host := &fixtureDraftHost{t: t, path: path, base: s.Creation.Repository.Commit, loseResponse: true}
	p, err := prepareDraft(ctx, path, "fixture/project", "refs/heads/main", "Initial", "Executed fixture", host)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executeDraft(ctx, path, p, effects.Authorization{}, host); err == nil || host.creates != 0 {
		t.Fatal("missing approval dispatched")
	}
	did, _ := p.Intent.ID()
	auth := effects.Authorization{IntentID: did, Actor: "fixture"}
	s, err = executeDraft(ctx, path, p, auth, host)
	if err == nil || s.Draft == nil || s.Draft.Outcome != "UNKNOWN" || host.creates != 1 {
		t.Fatal("lost response classification", err)
	}
	if _, err := executeDraft(ctx, path, p, auth, host); err == nil || host.creates != 1 {
		t.Fatal("creation retried")
	}
	// A real child exits with a kernel lease held after the pending draft.
	lock := filepath.Join(s.Creation.Repository.Root, ".harness", "leases", s.RunID+".lock")
	runDraftCrash(t, path, "acquired")
	if _, err := reconcileDraft(ctx, path, host); err == nil {
		t.Fatal("abandoned lease bypassed")
	}
	if _, err := PrepareDraftLeaseRecovery(path, "fixture owner stopped", false); err == nil {
		t.Fatal("missing quiescence accepted")
	}
	recovery, err := PrepareDraftLeaseRecovery(path, "fixture owner stopped", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recoverDraftLease(ctx, path, recovery, auth, host); err == nil {
		t.Fatal("draft authority reused for lease recovery")
	}
	for _, stage := range []string{"intent", "observed"} {
		runDraftCrash(t, path, stage)
		oldID, _ := recovery.Intent.ID()
		if _, err := recoverDraftLease(ctx, path, recovery, effects.Authorization{IntentID: oldID, Actor: "fixture"}, host); err == nil {
			t.Fatal("stale recovery predecessor accepted")
		}
		latest, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		previous, _ := latest.Draft.LeaseRecovery.Intent.Prepared.Intent.ID()
		recovery, err = PrepareDraftLeaseRecovery(path, "previous worker exited; no HTTP child exists", true)
		if err != nil {
			t.Fatal(err)
		}
		if recovery.Plan.PreviousIntentID != previous {
			t.Fatal("successor recovery lost predecessor")
		}
	}
	host.available = true
	recoveryID, _ := recovery.Intent.ID()
	s, err = recoverDraftLease(ctx, path, recovery, effects.Authorization{IntentID: recoveryID, Actor: "fixture"}, host)
	if err != nil || s.State != "HANDED_OFF" || s.Draft.Outcome != "CONFIRMED" || host.creates != 1 {
		t.Fatal("read-only recovery failed", err)
	}
	if s.Draft.LeaseRecovery == nil || s.Draft.LeaseRecovery.Outcome != "CONFIRMED" {
		t.Fatal("lease release not confirmed")
	}
	if _, err := os.Lstat(lock); !os.IsNotExist(err) {
		t.Fatal("lease retained", err)
	}
	if _, err := recoverDraftLease(ctx, path, recovery, effects.Authorization{IntentID: recoveryID, Actor: "fixture"}, host); err == nil {
		t.Fatal("lease authority repeated")
	}
	if _, err := reconcileDraft(ctx, path, host); err == nil {
		t.Fatal("terminal draft re-observed")
	}
}
