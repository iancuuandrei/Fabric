package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/verification"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// VerificationState retains the frozen plan, completed observations and pending launch.
type VerificationState struct {
	PlanID       string                    `json:"plan_id"`
	Plan         verification.Plan         `json:"plan"`
	Pending      bool                      `json:"pending"`
	Observations []VerificationObservation `json:"observations"`
	Closure      *VerificationClosure      `json:"closure"`
}

// VerificationClosure preserves an unknown result while recording the operator's
// explicit attestation that the interrupted workload and its descendants stopped.
// This is human evidence, not an inferred process outcome or successful check.
type VerificationClosure struct {
	PlanID           string             `json:"plan_id"`
	Actor            string             `json:"actor"`
	Evidence         string             `json:"evidence"`
	WorkloadsStopped bool               `json:"workloads_stopped"`
	Candidate        worktree.Candidate `json:"candidate"`
}

// VerificationStart binds a durable launch to one ordered check of one exact plan.
type VerificationStart struct {
	PlanID string `json:"plan_id"`
	Index  int    `json:"index"`
}

// VerificationObservation records process facts and independently observed source freshness.
type VerificationObservation struct {
	Start  VerificationStart   `json:"start"`
	Result verification.Result `json:"result"`
	After  FileObservation     `json:"after"`
}

// ErrAutonomousVerificationNotRun marks an autonomous run blocked because a
// required native check is unavailable or has not started. Callers may map
// this stable class to a safe user-facing diagnostic without exposing details.
var ErrAutonomousVerificationNotRun = errors.New("autonomous verification did not run")

// preflightAutonomousVerification resolves and hashes every required check
// against the exact candidate before autonomous role work can begin. Prepare
// is the same resolver used by the durable verification plan, so this does
// not introduce a second executable-selection policy.
func preflightAutonomousVerification(s Snapshot) error {
	if s.Creation.Execution == nil || s.Workspace == nil {
		return nil
	}
	if s.Candidate == nil {
		return errors.New("autonomous verification preflight requires a confirmed candidate")
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return err
	}
	return preflightVerificationEnvironment(s, candidateID, s.Workspace.Request.Path)
}

// preflightInitialVerification checks the initial environment before planning
// dispatch. Candidate-bound verification is still repeated after workspace creation.
func preflightInitialVerification(s Snapshot) error {
	if s.Creation.Execution == nil || s.State != "OBJECTIVE" && s.State != "PLANNING" {
		return nil
	}
	// Existing admitted planning effects must follow their recovery path.
	if s.State == "PLANNING" && (s.PlannerAccess != nil || s.PlannerHost != nil || s.PlannerReceipt != nil || s.PlannerProvider != nil || len(s.ModelAccess) != 0 || len(s.AgentDispatch) != 0) {
		return nil
	}
	r, err := worktree.Prepare(s.RunID, s.Creation.Repository)
	if err != nil {
		return err
	}
	if err := worktree.CheckDestination(r); err != nil {
		return err
	}
	id, err := s.Creation.Repository.ID()
	if err != nil {
		return errors.Join(ErrAutonomousUnsafe, err)
	}
	return preflightVerificationEnvironment(s, id, s.Creation.Repository.Root)
}

func preflightVerificationEnvironment(s Snapshot, sourceID, directory string) error {
	for _, check := range s.Creation.Config.Verification {
		invocation, err := verification.Prepare(sourceID, directory, check)
		if err != nil {
			return fmt.Errorf("autonomous verification check %q preflight failed: %w", check.Name, err)
		}
		if invocation.Executable == nil {
			return fmt.Errorf("%w: required check %q is unavailable before implementation: %s", ErrAutonomousVerificationNotRun, check.Name, invocation.Unavailable)
		}
	}
	return nil
}

// rejectUnstartedAutonomousVerification prevents a missing or otherwise
// unstarted native check from being treated as semantic test failure and
// consuming an autonomous repair slot. Started FAIL results remain eligible.
func rejectUnstartedAutonomousVerification(s Snapshot) error {
	if s.Verification == nil {
		return nil
	}
	for n, observation := range s.Verification.Observations {
		if observation.Result.Started || observation.Result.Status != "NOT_RUN" {
			continue
		}
		name := "unknown"
		if n < len(s.Verification.Plan.Invocations) {
			name = s.Verification.Plan.Invocations[n].Check.Name
		}
		return fmt.Errorf("%w: repair blocked because check %q did not start (NOT_RUN); this is not a semantic test failure", ErrAutonomousVerificationNotRun, name)
	}
	return nil
}

func replayVerification(s *Snapshot, e journal.Event, seen map[string]bool) error {
	switch e.Kind {
	case "verification.closed":
		v := s.Verification
		if s.State != "VERIFYING" || v == nil || v.Closure != nil {
			return errors.New("no interrupted verification plan to close")
		}
		var closure VerificationClosure
		if err := canonical.Decode(e.Payload, &closure); err != nil {
			return err
		}
		id, err := v.Plan.ID()
		if err != nil {
			return err
		}
		if closure.PlanID != id || strings.TrimSpace(closure.Actor) == "" || len(closure.Actor) > 256 || strings.TrimSpace(closure.Evidence) == "" || len(closure.Evidence) > 2048 || !closure.WorkloadsStopped {
			return errors.New("exact plan and explicit operator quiescence evidence required")
		}
		if s.Candidate == nil || closure.Candidate != *s.Candidate {
			return errors.New("closure cannot admit source changes")
		}
		v.Closure = &closure
		s.State = "REPAIRING"
	case "verification.planned":
		if err := filesAllowed(*s); err != nil {
			return err
		}
		var p verification.Plan
		if err := canonical.Decode(e.Payload, &p); err != nil {
			return err
		}
		if err := p.Validate(s.Creation.Config.Verification, s.Workspace.Request.Path); err != nil {
			return err
		}
		candidateID, err := s.Candidate.ID()
		if err != nil {
			return err
		}
		id, err := p.ID()
		if err != nil {
			return err
		}
		if p.RunID != s.RunID || p.CandidateID != candidateID || seen[id] {
			return errors.New("verification plan binding or attempt reuse rejected")
		}
		seen[id] = true
		s.Verification = &VerificationState{PlanID: id, Plan: p, Observations: []VerificationObservation{}}
		s.State = "VERIFYING"
	case "verification.started":
		v := s.Verification
		if s.State != "VERIFYING" || v == nil || v.Pending || len(v.Observations) >= len(v.Plan.Invocations) {
			return errors.New("verification launch transition rejected")
		}
		var start VerificationStart
		if err := canonical.Decode(e.Payload, &start); err != nil {
			return err
		}
		id, err := v.Plan.ID()
		if err != nil {
			return err
		}
		if start.PlanID != id || start.Index != len(v.Observations) {
			return errors.New("verification launch substitution")
		}
		v.Pending = true
	case "verification.observed":
		v := s.Verification
		if s.State != "VERIFYING" || v == nil || !v.Pending {
			return errors.New("no pending verification launch")
		}
		var o VerificationObservation
		if err := canonical.Decode(e.Payload, &o); err != nil {
			return err
		}
		id, err := v.Plan.ID()
		if err != nil {
			return err
		}
		n := len(v.Observations)
		if o.Start.PlanID != id || o.Start.Index != n {
			return errors.New("verification observation substitution")
		}
		if err := verification.ValidateResult(v.Plan.Invocations[n], o.Result); err != nil {
			return err
		}
		if len(o.After.Error) > 2048 || o.After.Candidate == nil && o.After.Error == "" {
			return errors.New("invalid verification source observation")
		}
		if o.After.Candidate != nil {
			if _, err := o.After.Candidate.ID(); err != nil {
				return err
			}
		}
		v.Observations = append(v.Observations, o)
		v.Pending = false
		if o.After.Candidate == nil || *o.After.Candidate != *s.Candidate || o.After.Error != "" {
			s.State = "REPAIRING"
			return nil
		}
		if len(v.Observations) == len(v.Plan.Invocations) {
			s.State = "READY"
			for _, result := range v.Observations {
				if result.Result.Status != "PASS" {
					s.State = "REPAIRING"
				}
			}
			if s.State == "READY" && s.Creation.Config.Reviewer != nil {
				s.State = "REVIEWING"
			}
		}
	}
	return nil
}

func verificationAllowed(s Snapshot) error {
	if s.State == "VERIFYING" && s.Verification != nil && !s.Verification.Pending && s.Verification.Closure == nil && s.Workspace != nil && s.Candidate != nil {
		return nil
	}
	return filesAllowed(s)
}

// CloseVerification records exact operator evidence for an interrupted attempt.
// It observes unchanged source under the writer lease and never kills a process,
// steals a stale lease, fabricates a missing result or dispatches another check.
func CloseVerification(ctx context.Context, path, planID, actor, evidence string, stopped bool) (snapshot Snapshot, err error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if s.State != "VERIFYING" || s.Workspace == nil {
		return s, errors.New("no interrupted verification")
	}
	lease, err := worktree.Acquire(s.Workspace.Request)
	if err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	current, err := observeCandidate(ctx, path, s)
	if err != nil {
		return s, err
	}
	if err = Append(path, "verification.closed", VerificationClosure{planID, actor, evidence, stopped, current}); err != nil {
		return s, err
	}
	return Inspect(path)
}

// Verify records every launch before execution, holds the writer lease, and
// admits READY only when all required checks pass on the unchanged candidate.
// A pending launch after interruption is never automatically repeated.
func Verify(ctx context.Context, path string) (snapshot Snapshot, err error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if err = requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	if err = verificationAllowed(s); err != nil {
		return s, err
	}
	lease, err := worktree.Acquire(s.Workspace.Request)
	if err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	if err = verificationAllowed(s); err != nil {
		return s, err
	}
	current, err := observeCandidate(ctx, path, s)
	if err != nil {
		return s, err
	}
	if current != *s.Candidate {
		return s, errors.New("verification requires exact admitted candidate")
	}
	id, err := current.ID()
	if err != nil {
		return s, err
	}
	var p verification.Plan
	first := 0
	if s.State == "VERIFYING" {
		p = s.Verification.Plan
		first = len(s.Verification.Observations)
	} else {
		nonce := make([]byte, 16)
		if _, err = rand.Read(nonce); err != nil {
			return s, err
		}
		p, err = verification.PreparePlan(s.RunID, hex.EncodeToString(nonce), id, s.Workspace.Request.Path, s.Creation.Config.Verification)
		if err != nil {
			return s, err
		}
		if err = Append(path, "verification.planned", p); err != nil {
			return s, err
		}
	}
	planID, err := p.ID()
	if err != nil {
		return s, err
	}
	if first == 0 {
		if hookErr := runLifecycleHooks(ctx, s, "pre_verify"); hookErr != nil {
			return s, hookErr
		}
	}
	for n := first; n < len(p.Invocations); n++ {
		invocation := p.Invocations[n]
		current, err = observeCandidate(ctx, path, s)
		if err != nil {
			return s, err
		}
		if current != *s.Candidate {
			return s, errors.New("candidate drift before verification launch")
		}
		start := VerificationStart{planID, n}
		if err = Append(path, "verification.started", start); err != nil {
			return s, err
		}
		result, runErr := verification.Execute(ctx, invocation)
		if runErr != nil {
			return s, runErr
		}
		observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		after, observeErr := observeCandidate(observeCtx, path, s)
		cancel()
		observation := FileObservation{Candidate: &after}
		if observeErr != nil {
			observation = FileObservation{Error: "candidate observation failed after verification"}
		}
		if err = Append(path, "verification.observed", VerificationObservation{start, result, observation}); err != nil {
			return s, err
		}
		s, err = Inspect(path)
		if err != nil {
			return s, err
		}
		if s.State != "VERIFYING" {
			return s, nil
		}
	}
	return Inspect(path)
}
