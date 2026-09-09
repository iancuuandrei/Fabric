package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitpush"
	"harness.local/engorch/internal/worktree"
)

// PreparedPush freezes an observed remote expectation and committed candidate.
type PreparedPush struct {
	Plan   gitpush.Plan   `json:"plan"`
	Intent effects.Intent `json:"intent"`
}

// PushIntent records exact operator authority before any push execution.
type PushIntent struct {
	Prepared      PreparedPush          `json:"prepared"`
	Authorization effects.Authorization `json:"authorization"`
}

// PushState retains an unresolved or independently observed publication attempt.
type PushState struct {
	LeaseRecovery *PushLeaseState `json:"lease_recovery,omitempty"`
	Intent        PushIntent      `json:"intent"`
	Outcome       string          `json:"outcome"`
	Observation   *PushReceipt    `json:"observation"`
}

func pushAllowed(s Snapshot) error {
	if s.State != "COMMITTED" || s.Push != nil || s.Commit == nil || s.Commit.Outcome != "CONFIRMED" || s.Workspace == nil || s.Candidate == nil || s.WorkspaceOutcome != "CONFIRMED" {
		return errors.New("push requires a confirmed committed candidate and no prior push")
	}
	if s.Commit.LeaseRecovery != nil && s.Commit.LeaseRecovery.Outcome != "CONFIRMED" {
		return errors.New("commit lease recovery is unresolved")
	}
	return nil
}

func validatePreparedPush(s Snapshot, prepared PreparedPush) error {
	if err := pushAllowed(s); err != nil {
		return err
	}
	repositoryID, err := s.Creation.Repository.ID()
	if err != nil {
		return err
	}
	p := prepared.Plan
	if p.RepositoryID != repositoryID || p.Workspace != *s.Workspace || p.Candidate != *s.Candidate || p.Candidate.Head != s.Commit.Intent.CommitID {
		return errors.New("push repository or committed candidate substituted")
	}
	intent, err := p.Intent(s.PlanID)
	if err != nil {
		return err
	}
	if intent != prepared.Intent {
		return errors.New("push intent substituted")
	}
	return nil
}

// PreparePush reads the exact remote expectation and committed candidate under
// lease. It does not persist an effect intent or authorize publication.
func PreparePush(ctx context.Context, path, destination, targetRef string, credentials ...*gitpush.Credential) (prepared PreparedPush, err error) {
	credential, err := pushCredential(destination, credentials)
	if err != nil {
		return prepared, err
	}
	s, err := Inspect(path)
	if err != nil {
		return prepared, err
	}
	if err := pushAllowed(s); err != nil {
		return prepared, err
	}
	lease, err := worktree.Acquire(s.Workspace.Request)
	if err != nil {
		return prepared, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return prepared, err
	}
	if err := pushAllowed(s); err != nil {
		return prepared, err
	}
	candidate, err := worktree.Pristine(ctx, *s.Workspace)
	if err != nil || candidate != *s.Candidate {
		return prepared, errors.Join(errors.New("push candidate changed"), err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return prepared, err
	}
	repositoryID, err := s.Creation.Repository.ID()
	if err != nil {
		return prepared, err
	}
	plan := gitpush.Plan{Version: 1, Nonce: hex.EncodeToString(nonce[:]), RepositoryID: repositoryID, Workspace: *s.Workspace, Candidate: candidate, Destination: destination, TargetRef: targetRef}
	remote, err := readPushRemote(ctx, plan, credential)
	if err != nil {
		return prepared, err
	}
	plan.ExpectedOld = remote.Commit
	intent, err := plan.Intent(s.PlanID)
	if err != nil {
		return prepared, err
	}
	return PreparedPush{plan, intent}, nil
}

// ExecutePush persists exact intent under the workspace lease, performs at most
// one push, then records independently observed state. Existing intents cannot
// be retried through this entry point; use read-only reconciliation.
func ExecutePush(ctx context.Context, path string, prepared PreparedPush, authorization effects.Authorization, credentials ...*gitpush.Credential) (snapshot Snapshot, err error) {
	credential, err := pushCredential(prepared.Plan.Destination, credentials)
	if err != nil {
		return snapshot, err
	}
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if err = requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	if err := validatePreparedPush(s, prepared); err != nil {
		return s, err
	}
	if err := authorization.Validate(prepared.Intent); err != nil {
		return s, err
	}
	lease, err := worktree.Acquire(prepared.Plan.Workspace.Request)
	if err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	if err := validatePreparedPush(s, prepared); err != nil {
		return s, err
	}
	candidate, err := worktree.Pristine(ctx, prepared.Plan.Workspace)
	if err != nil || candidate != prepared.Plan.Candidate {
		return s, errors.Join(errors.New("push candidate changed before intent"), err)
	}
	if err := Append(path, "push.intent", PushIntent{prepared, authorization}); err != nil {
		return s, err
	}
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	_, executionErr := runPush(ctx, prepared.Plan, prepared.Intent, authorization, credential)
	latest, observeErr := observePushLocked(context.WithoutCancel(ctx), path, s, credentials...)
	return latest, errors.Join(executionErr, observeErr)
}
