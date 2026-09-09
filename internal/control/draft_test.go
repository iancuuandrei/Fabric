package control

import (
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/draftpr"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitpush"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/worktree"
)

func TestDraftReplayAdmission(t *testing.T) {
	root := t.TempDir()
	source := repository.Identity{Version: 1, Name: "fixture", Root: root, CommonDir: filepath.Join(root, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	request, err := worktree.Prepare(strings.Repeat("c", 64), source)
	if err != nil {
		t.Fatal(err)
	}
	binding := worktree.Binding{Request: request, GitDir: filepath.Join(root, ".git", "worktrees", "fixture")}
	workspaceID, err := binding.ID()
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Repeat("3", 40)
	push := gitpush.Plan{Version: 1, Nonce: "push", RepositoryID: strings.Repeat("d", 64), Workspace: binding, Candidate: worktree.Candidate{Version: 1, WorktreeID: workspaceID, Head: source.Commit, IndexHash: strings.Repeat("e", 64), FilesHash: strings.Repeat("f", 64)}, Destination: "https://github.com/fixture/project.git", TargetRef: "refs/heads/initial", ExpectedOld: &old}
	pi, err := push.Intent(strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	p := draftpr.Plan{Version: 1, Nonce: "draft", Push: push, PushIntent: pi, Repository: "fixture/project", BaseRef: "refs/heads/main", BaseCommit: strings.Repeat("2", 40), Title: "Initial", Body: "Evidence"}
	di, err := p.Intent()
	if err != nil {
		t.Fatal(err)
	}
	id, _ := di.ID()
	s := Snapshot{RunID: di.RunID, PlanID: di.PlanID, State: "PUSHED", Workspace: &binding, Candidate: &push.Candidate, WorkspaceOutcome: "CONFIRMED", Push: &PushState{Outcome: "CONFIRMED", Intent: PushIntent{Prepared: PreparedPush{Plan: push, Intent: pi}}}}
	event := func(kind string, value any) journal.Event {
		raw, err := canonical.Bytes(value)
		if err != nil {
			t.Fatal(err)
		}
		return journal.Event{Kind: kind, Payload: raw}
	}
	intent := DraftIntent{Prepared: PreparedDraft{Plan: p, Intent: di}, Authorization: effects.Authorization{IntentID: id, Actor: "fixture"}}
	for _, scenario := range []string{"valid", "no-approval", "changed-push", "not-pushed", "unresolved-lease"} {
		t.Run(scenario, func(t *testing.T) {
			state := s
			proposed := intent
			pushState := *s.Push
			state.Push = &pushState
			if scenario == "no-approval" {
				proposed.Authorization.IntentID = ""
			}
			if scenario == "changed-push" {
				proposed.Prepared.Plan.Push.Nonce = "other"
			}
			if scenario == "not-pushed" {
				state.State = "COMMITTED"
			}
			if scenario == "unresolved-lease" {
				state.Push.LeaseRecovery = &PushLeaseState{Outcome: "UNKNOWN"}
			}
			err := replayDraft(&state, event("draft.intent", proposed))
			if scenario != "valid" {
				if err == nil {
					t.Fatal("invalid draft admitted")
				}
				return
			}
			if err != nil || state.State != "DRAFTING" {
				t.Fatalf("intent failed: %v", err)
			}
			if err := replayDraft(&state, event("draft.intent", proposed)); err == nil {
				t.Fatal("intent repeated")
			}
			r, _ := p.Request()
			remote := draftpr.Observation{Number: 7, URL: "https://github.com/fixture/project/pull/7", State: "open", Draft: true, Title: r.Title, Body: r.Body, Head: draftpr.BranchObservation{Repository: p.Repository, Ref: r.Head, Commit: push.Candidate.Head}, Base: draftpr.BranchObservation{Repository: p.Repository, Ref: r.Base, Commit: p.BaseCommit}}
			for _, outcome := range []string{"UNKNOWN", "NOT_APPLIED", "CONFIRMED"} {
				copyState := state
				copyDraft := *state.Draft
				copyState.Draft = &copyDraft
				o := DraftObservation{Error: "draft final state could not be verified"}
				if outcome == "CONFIRMED" {
					o = DraftObservation{Remote: &remote, Candidate: &push.Candidate}
				}
				hash, _ := canonical.Hash("harness.draft-observation.v1", o)
				err := replayDraft(&copyState, event("draft.observed", DraftReceipt{Receipt: effects.Receipt{Version: 1, IntentID: id, Outcome: outcome, ObservationHash: hash}, Observation: o}))
				if outcome == "NOT_APPLIED" {
					if err == nil {
						t.Fatal("absence proved non-execution")
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if outcome == "CONFIRMED" && copyState.State != "HANDED_OFF" {
					t.Fatal("confirmed draft not handed off")
				}
			}
		})
	}
}
