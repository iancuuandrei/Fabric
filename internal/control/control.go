package control

import (
	"errors"
	"strings"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexhost"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/worktree"
)

// Creation binds immutable inputs. Nonce distinguishes repeated human objectives.
type Creation struct {
	Version       int                 `json:"version"`
	Nonce         string              `json:"nonce"`
	Repository    repository.Identity `json:"repository"`
	Objective     string              `json:"objective"`
	Config        config.Config       `json:"config"`
	HostAdmission *HostAdmission      `json:"host_admission,omitempty"`
}

// Snapshot is reconstructed state, never independent authority to append effects.
type Snapshot struct {
	CandidateIndex     *CandidateIndexObservation         `json:"candidate_index,omitempty"`
	PlannerAccess      *access.Intent                     `json:"planner_access,omitempty"`
	ModelAccess        []ModelAccessState                 `json:"model_access,omitempty"`
	Draft              *DraftState                        `json:"draft,omitempty"`
	Push               *PushState                         `json:"push,omitempty"`
	Explorations       []ExplorerRecord                   `json:"explorations,omitempty"`
	ExplorerHost       *ExplorerHostState                 `json:"explorer_host,omitempty"`
	Commit             *CommitState                       `json:"commit,omitempty"`
	ReviewHost         *ReviewHostState                   `json:"review_host,omitempty"`
	Review             *ReviewRecord                      `json:"review,omitempty"`
	WriterHost         *WriterHostState                   `json:"writer_host,omitempty"`
	WriterProposal     *WriterRecord                      `json:"writer_proposal,omitempty"`
	RIProducer         *RIProducerState                   `json:"ri_producer"`
	RIPublish          *RIPublishState                    `json:"ri_publish"`
	RIImport           *RIImportState                     `json:"ri_import"`
	RILexical          *RILexicalState                    `json:"ri_lexical,omitempty"`
	RILexicalOverlay   *RILexicalOverlayState             `json:"ri_lexical_overlay,omitempty"`
	RunID              string                             `json:"run_id"`
	State              string                             `json:"state"`
	Creation           Creation                           `json:"creation"`
	PlanID             string                             `json:"plan_id"`
	Plan               *runtime.Result                    `json:"plan"`
	ApprovedBy         string                             `json:"approved_by"`
	WorkspaceIntent    *worktree.Request                  `json:"workspace_intent"`
	Workspace          *worktree.Binding                  `json:"workspace"`
	Candidate          *worktree.Candidate                `json:"candidate"`
	WorkspaceOutcome   string                             `json:"workspace_outcome"`
	FileIntent         *FileIntent                        `json:"file_intent"`
	FileReceipt        *FileReceipt                       `json:"file_receipt"`
	FileOutcome        string                             `json:"file_outcome"`
	FileRecovery       *RecoveryIntent                    `json:"file_recovery"`
	Verification       *VerificationState                 `json:"verification"`
	PlannerHost        *codexhost.Launch                  `json:"planner_host"`
	PlannerHostReady   bool                               `json:"planner_host_ready"`
	PlannerHostReceipt *codexhost.Receipt                 `json:"planner_host_receipt"`
	PlannerReceipt     *PlannerReceipt                    `json:"planner_receipt"`
	PlannerProvider    *providerDispatchReceipt           `json:"planner_provider,omitempty"`
	ProviderRuntime    map[string]providerDispatchReceipt `json:"provider_runtime,omitempty"`
	AgentDispatch      map[string]AgentDispatchState      `json:"agent_dispatch,omitempty"`
	Lifecycle          LifecycleState                     `json:"lifecycle"`
}

// Approval is explicit human input for one exact plan, not a model decision.
type Approval struct {
	PlanID string `json:"plan_id"`
	Actor  string `json:"actor"`
}

// Replay establishes all transition invariants from the complete journal. It
// rejects unknown events rather than returning a permissive partial state.
func Replay(events []journal.Event) (Snapshot, error) {
	s := Snapshot{}
	seenEffects := map[string]bool{}
	for _, e := range events {
		if err := admitLifecycleEvent(s, e.Kind); err != nil {
			return s, err
		}
		if !lifecycleControlEvent(e.Kind) && s.Draft != nil && s.Draft.LeaseRecovery != nil && s.Draft.LeaseRecovery.Outcome == "UNKNOWN" && e.Kind != "draft.observed" && e.Kind != "draft.lease-intent" && e.Kind != "draft.lease-observed" {
			return s, errors.New("draft lease recovery UNKNOWN; reconcile before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.Draft != nil && s.Draft.Outcome == "UNKNOWN" && e.Kind != "draft.observed" && e.Kind != "draft.lease-intent" && e.Kind != "draft.lease-observed" {
			return s, errors.New("draft UNKNOWN; reconcile before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.Push != nil && s.Push.LeaseRecovery != nil && s.Push.LeaseRecovery.Outcome == "UNKNOWN" && e.Kind != "push.observed" && e.Kind != "push.lease-intent" && e.Kind != "push.lease-observed" {
			return s, errors.New("push lease recovery UNKNOWN; reconcile before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.Push != nil && s.Push.Outcome == "UNKNOWN" && e.Kind != "push.observed" && e.Kind != "push.lease-intent" && e.Kind != "push.lease-observed" {
			return s, errors.New("push UNKNOWN; reconcile before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.Commit != nil && s.Commit.LeaseRecovery != nil && s.Commit.LeaseRecovery.Outcome == "UNKNOWN" && e.Kind != "commit.observed" && e.Kind != "commit.lease-observed" && e.Kind != "commit.lease-intent" {
			return s, errors.New("lease recovery UNKNOWN; reconcile before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.Commit != nil && s.Commit.Outcome == "UNKNOWN" && e.Kind != "commit.observed" && e.Kind != "commit.lease-intent" && e.Kind != "commit.lease-observed" && e.Kind != "commit.recovery-intent" {
			return s, errors.New("commit UNKNOWN; reconciliation required before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.RIProducer != nil && s.RIProducer.Outcome == "UNKNOWN" && s.RIProducer.Closure == nil && e.Kind != "ri.producer-observed" && e.Kind != "ri.producer-closed" {
			return s, errors.New("RI producer UNKNOWN; reconcile before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.RIPublish != nil && s.RIPublish.Outcome == "UNKNOWN" && e.Kind != "ri.publish-observed" && e.Kind != "ri.publish-recovery-intent" {
			return s, errors.New("RI publication UNKNOWN; reconcile before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.RIImport != nil && s.RIImport.Outcome == "UNKNOWN" && e.Kind != "ri.import-observed" {
			return s, errors.New("RI import UNKNOWN; reconcile before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.RILexical != nil && s.RILexical.Outcome == "UNKNOWN" && e.Kind != "ri.lexical-observed" {
			return s, errors.New("RI lexical UNKNOWN; reconciliation required")
		}
		if !lifecycleControlEvent(e.Kind) && s.RILexicalOverlay != nil && s.RILexicalOverlay.Outcome == "UNKNOWN" && e.Kind != "ri.overlay-observed" {
			return s, errors.New("RI overlay UNKNOWN; reconciliation required")
		}
		switch e.Kind {
		case "agent.dispatch-admitted", "agent.dispatch-observed":
			if err := replayAgentDispatch(&s, e); err != nil {
				return s, err
			}
		case "ri.overlay-intent", "ri.overlay-observed":
			if err := replayLexicalOverlay(&s, e, seenEffects); err != nil {
				return s, err
			}
		case "ri.lexical-intent", "ri.lexical-observed":
			if err := replayRILexical(&s, e, seenEffects); err != nil {
				return s, err
			}
		case "planning.access-intent":
			if err := replayPlanningAccess(&s, e); err != nil {
				return s, err
			}
		case "model.access-intent", "model.access-receipt":
			if err := replayModelAccess(&s, e); err != nil {
				return s, err
			}
		case "draft.lease-intent", "draft.lease-observed":
			if err := replayDraftLease(&s, e); err != nil {
				return s, err
			}
		case "draft.intent", "draft.observed":
			if err := replayDraft(&s, e); err != nil {
				return s, err
			}
		case "push.lease-intent", "push.lease-observed":
			if err := replayPushLease(&s, e); err != nil {
				return s, err
			}
		case "push.intent", "push.observed":
			if err := replayPush(&s, e); err != nil {
				return s, err
			}
		case "explorer.host-intent", "explorer.host-ready", "explorer.host-observed", "explorer.runtime-observed":
			if err := replayExplorerHost(&s, e); err != nil {
				return s, err
			}
			if e.Kind == "explorer.runtime-observed" {
				r := s.ExplorerHost.RuntimeReceipt
				if err := requireCompletedModelAccess(s, s.ExplorerHost.Intent.Invocation, r.JournalHead, r.ResultHash); err != nil {
					return s, err
				}
			}
		case "explorer.recorded":
			var record ExplorerRecord
			if err := canonical.Decode(e.Payload, &record); err != nil {
				return s, err
			}
			if err := replayExplorer(&s, record); err != nil {
				return s, err
			}
		case "commit.recovery-intent":
			if err := replayCommitRecovery(&s, e); err != nil {
				return s, err
			}
		case "commit.lease-intent", "commit.lease-observed":
			if err := replayCommitLease(&s, e); err != nil {
				return s, err
			}
		case "commit.observed":
			if err := replayCommitObservation(&s, e); err != nil {
				return s, err
			}
		case "commit.intent":
			if err := replayCommitIntent(&s, e); err != nil {
				return s, err
			}
		case "review.host-intent", "review.host-ready", "review.host-observed", "review.runtime-observed":
			if err := replayReviewHost(&s, e); err != nil {
				return s, err
			}
			if e.Kind == "review.runtime-observed" {
				r := s.ReviewHost.RuntimeReceipt
				if err := requireCompletedModelAccess(s, s.ReviewHost.Intent.Invocation, r.JournalHead, r.ResultHash); err != nil {
					return s, err
				}
			}
		case "review.recorded":
			if err := replayReview(&s, e); err != nil {
				return s, err
			}
		case "writer.host-intent", "writer.host-ready", "writer.host-observed", "writer.runtime-observed":
			if err := replayWriterHost(&s, e); err != nil {
				return s, err
			}
			if e.Kind == "writer.runtime-observed" {
				r := s.WriterHost.RuntimeReceipt
				if err := requireCompletedModelAccess(s, s.WriterHost.Intent.Invocation, r.JournalHead, r.ResultHash); err != nil {
					return s, err
				}
			}
		case "writer.proposed":
			if err := replayWriterProposal(&s, e, seenEffects); err != nil {
				return s, err
			}
		case "ri.producer-intent", "ri.producer-observed", "ri.producer-closed":
			if err := replayRIProducer(&s, e, seenEffects); err != nil {
				return s, err
			}
		case "ri.publish-intent", "ri.publish-observed", "ri.publish-recovery-intent":
			if err := replayRIPublish(&s, e, seenEffects); err != nil {
				return s, err
			}
		case "ri.import-intent", "ri.import-observed":
			if err := replayRIImport(&s, e, seenEffects); err != nil {
				return s, err
			}
		case "run.created":
			if s.State != "" {
				return s, errors.New("run already exists")
			}
			var c Creation
			if err := canonical.Decode(e.Payload, &c); err != nil {
				return s, err
			}
			if c.Version != 1 || strings.TrimSpace(c.Nonce) == "" || len(c.Nonce) > 128 || c.Config.Planner.Role != "planner" {
				return s, errors.New("invalid creation")
			}
			if err := c.Repository.Validate(); err != nil {
				return s, err
			}
			if err := c.Config.Validate(); err != nil {
				return s, err
			}
			if err := validateCreationHostAdmission(c); err != nil {
				return s, err
			}
			if c.Config.Repository != c.Repository.Name {
				return s, errors.New("configuration binding mismatch")
			}
			if _, err := plannerInvocation(c.Config, c.Objective); err != nil {
				return s, err
			}
			id, err := canonical.Hash("harness.run.v1", c)
			if err != nil {
				return s, err
			}
			s = Snapshot{RunID: id, State: "OBJECTIVE", Creation: c, WorkspaceOutcome: "NOT_RUN", Lifecycle: LifecycleState{Status: LifecycleActive}}
		case "run.pause-requested", "run.paused", "run.resumed", "run.cancel-requested", "run.cancelled":
			if err := replayLifecycle(&s, e); err != nil {
				return s, err
			}
		case "planning.started":
			if s.State != "OBJECTIVE" {
				return s, errors.New("planning transition rejected")
			}
			var empty struct{}
			if err := canonical.Decode(e.Payload, &empty); err != nil {
				return s, err
			}
			s.State = "PLANNING"
		case "plan.recorded":
			if s.State != "PLANNING" {
				return s, errors.New("plan transition rejected")
			}
			var r runtime.Result
			if err := canonical.Decode(e.Payload, &r); err != nil {
				return s, err
			}
			i, err := plannerInvocation(s.Creation.Config, s.Creation.Objective)
			if err != nil {
				return s, err
			}
			if err = runtime.ValidateResult(i, r, true); err != nil {
				return s, err
			}
			if s.Creation.Config.Version == 2 && s.Creation.Config.Planner.Runtime == "fake" {
				if err := validatePlanningAccessResult(s, r); err != nil {
					return s, err
				}
			}
			if s.Creation.Config.Planner.Runtime == "codex-app-server" {
				hash, err := canonical.Hash("harness.planner-result.v1", r)
				if err != nil {
					return s, err
				}
				if s.PlannerReceipt == nil || s.PlannerReceipt.ResultHash != hash {
					return s, errors.New("plan lacks exact runtime receipt")
				}
			}
			if s.Creation.Config.Planner.Runtime == "provider-api" || s.Creation.Config.Planner.Runtime == "opencode-http" {
				hash, err := canonical.Hash("harness.planner-result.v1", r)
				if err != nil {
					return s, err
				}
				if s.PlannerProvider == nil || s.PlannerProvider.ResultHash != hash || !sameCanonical(s.PlannerProvider.Result, r) {
					return s, errors.New("plan lacks exact direct provider receipt")
				}
			}
			id, err := canonical.Hash("harness.plan.v1", r)
			if err != nil {
				return s, err
			}
			s.Plan = &r
			s.PlanID = id
			s.State = "AWAITING_APPROVAL"
		case "plan.approved":
			if s.State != "AWAITING_APPROVAL" {
				return s, errors.New("approval transition rejected")
			}
			var a Approval
			if err := canonical.Decode(e.Payload, &a); err != nil {
				return s, err
			}
			if a.PlanID != s.PlanID || strings.TrimSpace(a.Actor) == "" || len(a.Actor) > 256 {
				return s, errors.New("approval does not bind exact plan and actor")
			}
			s.ApprovedBy = a.Actor
			s.State = "IMPLEMENTING"
		case "candidate.index-observed":
			if err := replayCandidateIndex(&s, e.Payload); err != nil {
				return s, err
			}
		case "workspace.intent":
			if s.State != "IMPLEMENTING" || s.WorkspaceIntent != nil || s.Workspace != nil {
				return s, errors.New("workspace intent transition rejected")
			}
			var r worktree.Request
			if err := canonical.Decode(e.Payload, &r); err != nil {
				return s, err
			}
			if err := r.Validate(); err != nil {
				return s, err
			}
			expectedStateRoot, err := controllerNamespace(s.Creation)
			if err != nil || r.RunID != s.RunID || r.Source != s.Creation.Repository || r.CandidateIdentity != s.Creation.Config.CandidateIdentity || r.ControllerStateRoot != expectedStateRoot {
				return s, errors.New("workspace intent binding mismatch")
			}
			s.WorkspaceIntent = &r
			s.WorkspaceOutcome = "UNKNOWN"
		case "workspace.confirmed":
			if s.State != "IMPLEMENTING" || s.WorkspaceIntent == nil || s.Workspace != nil {
				return s, errors.New("workspace receipt transition rejected")
			}
			var receipt WorkspaceReceipt
			if err := canonical.Decode(e.Payload, &receipt); err != nil {
				return s, err
			}
			if receipt.Binding.Request != *s.WorkspaceIntent {
				return s, errors.New("workspace receipt substitution")
			}
			id, err := receipt.Binding.ID()
			if err != nil {
				return s, err
			}
			if _, err := receipt.Candidate.ID(); err != nil {
				return s, err
			}
			expectedVersion := 1
			if s.Creation.Config.CandidateIdentity == "semantic-index-v2" {
				expectedVersion = 2
			}
			if receipt.Candidate.Version != expectedVersion {
				return s, errors.New("candidate identity version differs from authorized config")
			}
			if receipt.Candidate.WorktreeID != id || receipt.Candidate.Head != s.Creation.Repository.Commit {
				return s, errors.New("workspace candidate binding mismatch")
			}
			s.Workspace = &receipt.Binding
			s.Candidate = &receipt.Candidate
			s.WorkspaceOutcome = "CONFIRMED"
		case "files.intent":
			if err := filesAllowed(s); err != nil {
				return s, err
			}
			var intent FileIntent
			if err := canonical.Decode(e.Payload, &intent); err != nil {
				return s, err
			}
			expected, err := preparedFiles(s, intent.Prepared.Proposal)
			if err != nil {
				return s, err
			}
			if expected.Intent != intent.Prepared.Intent {
				return s, errors.New("file effect intent binding mismatch")
			}
			if err = intent.Authorization.Validate(expected.Intent); err != nil {
				return s, err
			}
			id, err := expected.Intent.ID()
			if err != nil {
				return s, err
			}
			if seenEffects[id] {
				return s, errors.New("effect intent already attempted")
			}
			seenEffects[id] = true
			s.FileIntent = &intent
			s.FileRecovery = nil
			s.FileReceipt = nil
			s.FileOutcome = "UNKNOWN"
		case "files.recovery-intent":
			var intent RecoveryIntent
			if err := canonical.Decode(e.Payload, &intent); err != nil {
				return s, err
			}
			expected, err := preparedRecovery(s, intent.Prepared.Recovery)
			if err != nil {
				return s, err
			}
			if expected.Intent != intent.Prepared.Intent {
				return s, errors.New("recovery effect binding mismatch")
			}
			if err = intent.Authorization.Validate(expected.Intent); err != nil {
				return s, err
			}
			id, err := expected.Intent.ID()
			if err != nil {
				return s, err
			}
			if seenEffects[id] {
				return s, errors.New("recovery already attempted")
			}
			if s.FileReceipt == nil || s.FileReceipt.Observation.Candidate == nil || *s.FileReceipt.Observation.Candidate != intent.Prepared.Recovery.Observed {
				return s, errors.New("fresh reconciliation receipt required before recovery")
			}
			seenEffects[id] = true
			s.FileRecovery = &intent
			s.FileReceipt = nil
		case "files.observed":
			if s.FileIntent == nil || s.FileOutcome != "UNKNOWN" {
				return s, errors.New("no pending file effect")
			}
			var receipt FileReceipt
			if err := canonical.Decode(e.Payload, &receipt); err != nil {
				return s, err
			}
			if len(receipt.Observation.Error) > 2048 || receipt.Observation.Candidate == nil && receipt.Observation.Error == "" {
				return s, errors.New("invalid file observation")
			}
			if receipt.Observation.Candidate != nil {
				if _, err := receipt.Observation.Candidate.ID(); err != nil {
					return s, err
				}
			}
			h, err := canonical.Hash("harness.file-observation.v1", receipt.Observation)
			if err != nil {
				return s, err
			}
			if h != receipt.Receipt.ObservationHash {
				return s, errors.New("file observation hash mismatch")
			}
			outcome, err := effects.Outcome(activeFileIntent(s), &receipt.Receipt)
			if err != nil {
				return s, err
			}
			if outcome != fileeffects.Classify(s.FileIntent.Prepared.Proposal, receipt.Observation.Candidate) {
				return s, errors.New("file outcome not established by observation")
			}
			s.FileReceipt = &receipt
			s.FileOutcome = outcome
			if outcome == "CONFIRMED" {
				after := s.FileIntent.Prepared.Proposal.After
				s.Candidate = &after
			}
		case "planning.host-intent", "planning.host-ready", "planning.host-observed", "planning.runtime-observed":
			if err := replayPlanner(&s, e); err != nil {
				return s, err
			}
		case "planning.provider-observed":
			if err := replayPlannerProvider(&s, e); err != nil {
				return s, err
			}
		case "role.provider-observed":
			if err := replayRoleProvider(&s, e); err != nil {
				return s, err
			}
		case "verification.planned", "verification.started", "verification.observed", "verification.closed":
			if err := replayVerification(&s, e, seenEffects); err != nil {
				return s, err
			}
		default:
			return s, errors.New("unknown controller event")
		}
	}
	return s, nil
}

// Append validates semantic state inside journal.Append's exclusive lock. This
// prevents two callers from both authorizing transitions against a stale read.
func Append(path, kind string, payload any) error {
	if kind == "model.access-receipt" {
		return errors.New("model access receipts require runtime journal reconciliation")
	}
	if kind == "run.created" {
		if err := validateControllerCreationPath(path, payload); err != nil {
			return err
		}
	}
	_, err := journal.Append(path, kind, payload, func(events []journal.Event) error {
		s, err := Replay(events)
		if err != nil {
			return err
		}
		return validateControllerJournalPath(path, s.Creation, s.RunID)
	})
	return err
}

// Inspect validates integrity and semantic history before returning a snapshot.
func Inspect(path string) (Snapshot, error) {
	events, err := journal.Read(path)
	if err != nil {
		return Snapshot{}, err
	}
	s, err := Replay(events)
	if err != nil {
		return s, err
	}
	return s, validateControllerJournalPath(path, s.Creation, s.RunID)
}
