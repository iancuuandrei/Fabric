package control

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
)

const (
	// LifecycleActive permits new controller dispatch admissions.
	LifecycleActive = "ACTIVE"
	// LifecyclePauseRequested blocks new dispatch while admitted work settles.
	LifecyclePauseRequested = "PAUSE_REQUESTED"
	// LifecyclePaused is a quiescence-attested, resumable stop.
	LifecyclePaused = "PAUSED"
	// LifecycleCancelRequested blocks new dispatch while admitted work stops.
	LifecycleCancelRequested = "CANCEL_REQUESTED"
	// LifecycleCancelled is terminal and cannot be resumed.
	LifecycleCancelled = "CANCELLED"
)

// LifecycleState is replayed from the controller journal. A zero value on a
// pre-lifecycle v1 history means ACTIVE; old journals require no migration.
type LifecycleState struct {
	Status        string               `json:"status"`
	PauseRequest  *LifecycleRequest    `json:"pause_request,omitempty"`
	CancelRequest *LifecycleRequest    `json:"cancel_request,omitempty"`
	Settlement    *LifecycleSettlement `json:"settlement,omitempty"`
	LastResume    *LifecycleResume     `json:"last_resume,omitempty"`
}

// LifecycleRequest is explicit operator input. Nonce distinguishes repeated
// requests without using wall-clock values as authority.
type LifecycleRequest struct {
	Version int    `json:"version"`
	RunID   string `json:"run_id"`
	Action  string `json:"action"`
	Actor   string `json:"actor"`
	Nonce   string `json:"nonce"`
}

// ID returns the canonical identity of this exact pause or cancel request.
func (r LifecycleRequest) ID() (string, error) {
	if r.Action != "pause" && r.Action != "cancel" {
		return "", errors.New("unknown lifecycle request action")
	}
	if err := r.validate(r.RunID, r.Action); err != nil {
		return "", err
	}
	return canonical.Hash("harness.run-lifecycle-request.v1", r)
}

func (r LifecycleRequest) validate(runID, action string) error {
	if r.Version != 1 || r.RunID != runID || safepath.RequireDigest(r.RunID) != nil || r.Action != action ||
		strings.TrimSpace(r.Actor) == "" || len(r.Actor) > 256 ||
		strings.TrimSpace(r.Nonce) == "" || len(r.Nonce) > 128 {
		return errors.New("invalid lifecycle request")
	}
	return nil
}

// LifecycleSettlement is an explicit quiescence attestation for one request.
// Unresolved retains the exact controller-known work whose effect/result is
// still UNKNOWN; settling the lifecycle does not promote or discard it.
type LifecycleSettlement struct {
	Version          int      `json:"version"`
	RunID            string   `json:"run_id"`
	RequestID        string   `json:"request_id"`
	ControllerHead   string   `json:"controller_head"`
	Actor            string   `json:"actor"`
	Evidence         string   `json:"evidence"`
	WorkloadsStopped bool     `json:"workloads_stopped"`
	Unresolved       []string `json:"unresolved"`
}

// LifecycleResume reopens dispatch after a fully settled pause. It creates no
// runtime/effect intent and therefore cannot resend previously admitted work.
type LifecycleResume struct {
	Version        int    `json:"version"`
	RunID          string `json:"run_id"`
	PauseRequestID string `json:"pause_request_id"`
	Actor          string `json:"actor"`
	Nonce          string `json:"nonce"`
}

// ID returns the canonical identity of this exact resume instruction.
func (r LifecycleResume) ID() (string, error) {
	if r.Version != 1 || safepath.RequireDigest(r.RunID) != nil ||
		safepath.RequireDigest(r.PauseRequestID) != nil ||
		strings.TrimSpace(r.Actor) == "" || len(r.Actor) > 256 ||
		strings.TrimSpace(r.Nonce) == "" || len(r.Nonce) > 128 {
		return "", errors.New("invalid lifecycle resume")
	}
	return canonical.Hash("harness.run-lifecycle-resume.v1", r)
}

func lifecycleStatus(s Snapshot) string {
	if s.Lifecycle.Status == "" {
		return LifecycleActive
	}
	return s.Lifecycle.Status
}

// RequireDispatchAllowed is the routing hook for paths which dispatch without
// first appending a controller intent. Intent-first paths are also protected by
// admitLifecycleEvent under the journal lock.
func RequireDispatchAllowed(s Snapshot) error {
	if status := lifecycleStatus(s); status != LifecycleActive {
		return fmt.Errorf("run lifecycle %s blocks new dispatch", status)
	}
	return nil
}

// admitLifecycleEvent is evaluated before every controller event. While a run
// is stopping or stopped, only exact receipts, observations, read-only closure
// and lifecycle events may advance replay. Any new event kind fails closed.
func admitLifecycleEvent(s Snapshot, kind string) error {
	status := lifecycleStatus(s)
	if status == LifecycleActive || kind == "run.pause-requested" || kind == "run.cancel-requested" {
		return nil
	}
	if lifecycleSettlementEvent(kind) || lifecycleReconciliationEvent(kind) {
		return nil
	}
	return fmt.Errorf("run lifecycle %s rejects event %q", status, kind)
}

func lifecycleSettlementEvent(kind string) bool {
	switch kind {
	case "run.paused", "run.resumed", "run.cancelled":
		return true
	default:
		return false
	}
}

func lifecycleControlEvent(kind string) bool {
	switch kind {
	case "run.pause-requested", "run.paused", "run.resumed", "run.cancel-requested", "run.cancelled":
		return true
	default:
		return false
	}
}

func lifecycleReconciliationEvent(kind string) bool {
	switch kind {
	case "model.access-receipt", "candidate.index-observed",
		"agent.dispatch-observed",
		"planning.host-ready", "planning.host-observed", "planning.runtime-observed", "planning.provider-observed", "plan.recorded",
		"explorer.host-ready", "explorer.host-observed", "explorer.runtime-observed", "explorer.recorded",
		"writer.host-ready", "writer.host-observed", "writer.runtime-observed", "writer.proposed",
		"review.host-ready", "review.host-observed", "review.runtime-observed", "review.recorded",
		"role.provider-observed", "workspace.confirmed", "files.observed",
		"verification.observed", "verification.closed",
		"commit.observed", "commit.lease-observed",
		"push.observed", "push.lease-observed",
		"draft.observed", "draft.lease-observed",
		"ri.producer-observed", "ri.producer-closed", "ri.publish-observed",
		"ri.import-observed", "ri.lexical-observed", "ri.overlay-observed":
		return true
	default:
		return false
	}
}

func replayLifecycle(s *Snapshot, event journal.Event) error {
	if s.RunID == "" {
		return errors.New("lifecycle event requires a run")
	}
	switch event.Kind {
	case "run.pause-requested":
		if lifecycleStatus(*s) != LifecycleActive {
			return errors.New("pause request transition rejected")
		}
		var request LifecycleRequest
		if err := canonical.Decode(event.Payload, &request); err != nil {
			return err
		}
		if err := request.validate(s.RunID, "pause"); err != nil {
			return err
		}
		s.Lifecycle.Status = LifecyclePauseRequested
		s.Lifecycle.PauseRequest = &request
		s.Lifecycle.Settlement = nil
	case "run.paused":
		if lifecycleStatus(*s) != LifecyclePauseRequested || s.Lifecycle.PauseRequest == nil {
			return errors.New("pause settlement transition rejected")
		}
		if err := replayLifecycleSettlement(s, event, *s.Lifecycle.PauseRequest); err != nil {
			return err
		}
		s.Lifecycle.Status = LifecyclePaused
	case "run.resumed":
		if lifecycleStatus(*s) != LifecyclePaused || s.Lifecycle.PauseRequest == nil {
			return errors.New("resume transition rejected")
		}
		var resume LifecycleResume
		if err := canonical.Decode(event.Payload, &resume); err != nil {
			return err
		}
		pauseID, err := s.Lifecycle.PauseRequest.ID()
		if err != nil || resume.RunID != s.RunID || resume.PauseRequestID != pauseID {
			return errors.New("resume does not bind settled pause")
		}
		if _, err = resume.ID(); err != nil {
			return err
		}
		s.Lifecycle.Status = LifecycleActive
		s.Lifecycle.LastResume = &resume
	case "run.cancel-requested":
		status := lifecycleStatus(*s)
		if status == LifecycleCancelRequested || status == LifecycleCancelled {
			return errors.New("cancel request transition rejected")
		}
		var request LifecycleRequest
		if err := canonical.Decode(event.Payload, &request); err != nil {
			return err
		}
		if err := request.validate(s.RunID, "cancel"); err != nil {
			return err
		}
		s.Lifecycle.Status = LifecycleCancelRequested
		s.Lifecycle.CancelRequest = &request
		s.Lifecycle.Settlement = nil
	case "run.cancelled":
		if lifecycleStatus(*s) != LifecycleCancelRequested || s.Lifecycle.CancelRequest == nil {
			return errors.New("cancel settlement transition rejected")
		}
		if err := replayLifecycleSettlement(s, event, *s.Lifecycle.CancelRequest); err != nil {
			return err
		}
		s.Lifecycle.Status = LifecycleCancelled
	default:
		return errors.New("unknown lifecycle event")
	}
	return nil
}

func replayLifecycleSettlement(s *Snapshot, event journal.Event, request LifecycleRequest) error {
	var settlement LifecycleSettlement
	if err := canonical.Decode(event.Payload, &settlement); err != nil {
		return err
	}
	requestID, err := request.ID()
	if err != nil {
		return err
	}
	unresolved := lifecycleUnresolved(*s)
	if settlement.Version != 1 || settlement.RunID != s.RunID || settlement.RequestID != requestID ||
		settlement.ControllerHead != event.Previous || strings.TrimSpace(settlement.Actor) == "" || len(settlement.Actor) > 256 ||
		strings.TrimSpace(settlement.Evidence) == "" || len(settlement.Evidence) > 2048 || !settlement.WorkloadsStopped ||
		!slices.Equal(settlement.Unresolved, unresolved) {
		return errors.New("lifecycle settlement lacks exact quiescence evidence")
	}
	copy := settlement
	copy.Unresolved = append([]string{}, settlement.Unresolved...)
	s.Lifecycle.Settlement = &copy
	return nil
}

// lifecycleUnresolved returns stable controller-known uncertainty. It is
// retained in pause/cancel evidence; it does not assert that a process is alive.
func lifecycleUnresolved(s Snapshot) []string {
	result := []string{}
	if s.State == "PLANNING" && s.Plan == nil {
		result = append(result, "planning")
	}
	for invocationID, dispatch := range s.AgentDispatch {
		if dispatch.Observation == nil || dispatch.Observation.Status == agenttree.StatusUnknown {
			result = append(result, "agent-dispatch:"+invocationID)
		}
	}
	for _, access := range s.ModelAccess {
		if access.Terminal == nil {
			result = append(result, "model-access:"+access.RuntimeInvocationID)
		}
	}
	if s.ExplorerHost != nil && s.ExplorerHost.RuntimeReceipt == nil {
		result = append(result, "explorer:"+s.ExplorerHost.Intent.Invocation.ID)
	}
	if s.WriterHost != nil && s.WriterHost.RuntimeReceipt == nil {
		result = append(result, "writer:"+s.WriterHost.Intent.Invocation.ID)
	}
	if s.ReviewHost != nil && s.ReviewHost.RuntimeReceipt == nil {
		result = append(result, "review:"+s.ReviewHost.Intent.Invocation.ID)
	}
	if s.WorkspaceOutcome == "UNKNOWN" {
		result = append(result, "workspace")
	}
	if s.FileOutcome == "UNKNOWN" {
		result = append(result, "files")
	}
	if s.Verification != nil && s.Verification.Pending && s.Verification.Closure == nil {
		result = append(result, "verification")
	}
	appendEffect := func(name, outcome string) {
		if outcome == "UNKNOWN" {
			result = append(result, name)
		}
	}
	if s.Commit != nil {
		appendEffect("commit", s.Commit.Outcome)
		if s.Commit.Recovery != nil {
			appendEffect("commit-recovery", s.Commit.Recovery.Outcome)
		}
		if s.Commit.LeaseRecovery != nil {
			appendEffect("commit-lease", s.Commit.LeaseRecovery.Outcome)
		}
	}
	if s.Push != nil {
		appendEffect("push", s.Push.Outcome)
		if s.Push.LeaseRecovery != nil {
			appendEffect("push-lease", s.Push.LeaseRecovery.Outcome)
		}
	}
	if s.Draft != nil {
		appendEffect("draft", s.Draft.Outcome)
		if s.Draft.LeaseRecovery != nil {
			appendEffect("draft-lease", s.Draft.LeaseRecovery.Outcome)
		}
	}
	if s.RIProducer != nil && s.RIProducer.Closure == nil {
		appendEffect("ri-producer", s.RIProducer.Outcome)
	}
	if s.RIPublish != nil {
		appendEffect("ri-publish", s.RIPublish.Outcome)
	}
	if s.RIImport != nil {
		appendEffect("ri-import", s.RIImport.Outcome)
	}
	if s.RILexical != nil {
		appendEffect("ri-lexical", s.RILexical.Outcome)
	}
	if s.RILexicalOverlay != nil {
		appendEffect("ri-overlay", s.RILexicalOverlay.Outcome)
	}
	slices.Sort(result)
	return result
}

// RequestPause durably closes admission after the currently admitted stage.
func RequestPause(path, actor, nonce string) (Snapshot, error) {
	return requestLifecycle(path, "pause", actor, nonce)
}

// RequestCancel durably closes admission and begins terminal cancellation.
func RequestCancel(path, actor, nonce string) (Snapshot, error) {
	return requestLifecycle(path, "cancel", actor, nonce)
}

func requestLifecycle(path, action, actor, nonce string) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	request := LifecycleRequest{Version: 1, RunID: s.RunID, Action: action, Actor: actor, Nonce: nonce}
	if _, err = request.ID(); err != nil {
		return s, err
	}
	var recorded *LifecycleRequest
	if action == "pause" {
		recorded = s.Lifecycle.PauseRequest
	} else {
		recorded = s.Lifecycle.CancelRequest
	}
	if recorded != nil && *recorded == request {
		return s, nil
	}
	kind := "run." + action + "-requested"
	if err = Append(path, kind, request); err != nil {
		latest, inspectErr := Inspect(path)
		if inspectErr != nil {
			return latest, errors.Join(err, inspectErr)
		}
		recorded = nil
		if action == "pause" {
			recorded = latest.Lifecycle.PauseRequest
		} else {
			recorded = latest.Lifecycle.CancelRequest
		}
		if recorded != nil && *recorded == request {
			return latest, nil
		}
		return latest, err
	}
	return Inspect(path)
}

// SettleLifecycle records external evidence that the admitted stage and all
// descendants stopped. It performs no cancellation, process inspection, retry
// or effect dispatch.
func SettleLifecycle(path, actor, evidence string, workloadsStopped bool) (Snapshot, error) {
	events, err := journal.Read(path)
	if err != nil {
		return Snapshot{}, err
	}
	s, err := Replay(events)
	if err != nil {
		return s, err
	}
	if err := validateControllerJournalPath(path, s.Creation, s.RunID); err != nil {
		return s, err
	}
	if (lifecycleStatus(s) == LifecyclePaused || lifecycleStatus(s) == LifecycleCancelled) && s.Lifecycle.Settlement != nil {
		settlement := s.Lifecycle.Settlement
		if settlement.Actor == actor && settlement.Evidence == evidence && settlement.WorkloadsStopped == workloadsStopped {
			return s, nil
		}
	}
	var request *LifecycleRequest
	kind := ""
	switch lifecycleStatus(s) {
	case LifecyclePauseRequested:
		request, kind = s.Lifecycle.PauseRequest, "run.paused"
	case LifecycleCancelRequested:
		request, kind = s.Lifecycle.CancelRequest, "run.cancelled"
	default:
		return s, errors.New("lifecycle has no request to settle")
	}
	if request == nil || len(events) == 0 {
		return s, errors.New("lifecycle request evidence missing")
	}
	requestID, err := request.ID()
	if err != nil {
		return s, err
	}
	settlement := LifecycleSettlement{Version: 1, RunID: s.RunID, RequestID: requestID, ControllerHead: events[len(events)-1].Hash, Actor: actor, Evidence: evidence, WorkloadsStopped: workloadsStopped, Unresolved: lifecycleUnresolved(s)}
	if err = Append(path, kind, settlement); err != nil {
		latest, inspectErr := Inspect(path)
		if inspectErr != nil {
			return latest, errors.Join(err, inspectErr)
		}
		if latest.Lifecycle.Settlement != nil && sameCanonical(*latest.Lifecycle.Settlement, settlement) {
			return latest, nil
		}
		return latest, err
	}
	return Inspect(path)
}

// Resume reopens dispatch for one exact settled pause without resending work.
func Resume(path, actor, nonce string) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if lifecycleStatus(s) == LifecycleActive && s.Lifecycle.LastResume != nil && s.Lifecycle.LastResume.Actor == actor && s.Lifecycle.LastResume.Nonce == nonce {
		return s, nil
	}
	if lifecycleStatus(s) != LifecyclePaused || s.Lifecycle.PauseRequest == nil {
		return s, errors.New("settled pause required for resume")
	}
	pauseID, err := s.Lifecycle.PauseRequest.ID()
	if err != nil {
		return s, err
	}
	resume := LifecycleResume{Version: 1, RunID: s.RunID, PauseRequestID: pauseID, Actor: actor, Nonce: nonce}
	if _, err = resume.ID(); err != nil {
		return s, err
	}
	if err = Append(path, "run.resumed", resume); err != nil {
		latest, inspectErr := Inspect(path)
		if inspectErr != nil {
			return latest, errors.Join(err, inspectErr)
		}
		if latest.Lifecycle.LastResume != nil && *latest.Lifecycle.LastResume == resume {
			return latest, nil
		}
		return latest, err
	}
	return Inspect(path)
}
