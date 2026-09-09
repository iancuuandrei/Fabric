package control

import (
	"context"
	"errors"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitpush"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/worktree"
)

// PushObservation retains remote readback and exact local candidate freshness.
type PushObservation struct {
	Remote    *gitpush.RefObservation `json:"remote"`
	Candidate *worktree.Candidate     `json:"candidate"`
	Error     string                  `json:"error"`
}

// PushReceipt binds publication classification to independently observed state.
type PushReceipt struct {
	Receipt     effects.Receipt `json:"receipt"`
	Observation PushObservation `json:"observation"`
}

func replayPush(s *Snapshot, event journal.Event) error {
	if event.Kind == "push.intent" {
		var intent PushIntent
		if err := canonical.Decode(event.Payload, &intent); err != nil {
			return err
		}
		if err := validatePreparedPush(*s, intent.Prepared); err != nil {
			return err
		}
		if err := intent.Authorization.Validate(intent.Prepared.Intent); err != nil {
			return err
		}
		s.Push = &PushState{Intent: intent, Outcome: "UNKNOWN"}
		s.State = "PUSHING"
		return nil
	}
	if s.Push == nil || s.Push.Outcome != "UNKNOWN" || s.State != "PUSHING" {
		return errors.New("pending push required")
	}
	var receipt PushReceipt
	if err := canonical.Decode(event.Payload, &receipt); err != nil {
		return err
	}
	o := receipt.Observation
	p := s.Push.Intent.Prepared
	hash, err := canonical.Hash("harness.push-observation.v1", o)
	if err != nil {
		return err
	}
	if hash != receipt.Receipt.ObservationHash {
		return errors.New("push observation hash mismatch")
	}
	outcome, err := effects.Outcome(p.Intent, &receipt.Receipt)
	if err != nil {
		return err
	}
	if o.Remote != nil {
		if o.Remote.TargetRef != p.Plan.TargetRef {
			return errors.New("foreign remote ref observation")
		}
		if o.Remote.Commit != nil {
			if _, err := gitpush.ParseAdvertisement(p.Plan, []byte(*o.Remote.Commit+"\t"+o.Remote.TargetRef+"\n")); err != nil {
				return err
			}
		}
	}
	if o.Candidate != nil && *o.Candidate != p.Plan.Candidate {
		return errors.New("push candidate observation mismatch")
	}
	confirmed := o.Remote != nil && o.Remote.Commit != nil && *o.Remote.Commit == p.Plan.Candidate.Head && o.Candidate != nil
	if confirmed {
		if outcome != "CONFIRMED" || o.Error != "" {
			return errors.New("confirmed push classification mismatch")
		}
		s.State = "PUSHED"
	} else if outcome != "UNKNOWN" || o.Error != "push final state could not be verified" {
		return errors.New("unresolved push cannot prove non-execution")
	}
	s.Push.Outcome, s.Push.Observation = outcome, &receipt
	return nil
}

// ReconcilePush observes an existing intent without performing another push.
func ReconcilePush(ctx context.Context, path string, credentials ...*gitpush.Credential) (snapshot Snapshot, err error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if s.Push == nil || s.Push.Outcome != "UNKNOWN" {
		return s, errors.New("pending push required")
	}
	lease, err := worktree.Acquire(s.Push.Intent.Prepared.Plan.Workspace.Request)
	if err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	if s.Push == nil || s.Push.Outcome != "UNKNOWN" {
		return s, errors.New("push changed before observation")
	}
	return observePushLocked(ctx, path, s, credentials...)
}

func observePushLocked(ctx context.Context, path string, s Snapshot, credentials ...*gitpush.Credential) (Snapshot, error) {
	credential, err := pushCredential(s.Push.Intent.Prepared.Plan.Destination, credentials)
	if err != nil {
		return s, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	p := s.Push.Intent.Prepared
	observation := PushObservation{}
	remote, remoteErr := readPushRemote(ctx, p.Plan, credential)
	if remoteErr == nil {
		observation.Remote = &remote
	}
	candidate, candidateErr := worktree.Pristine(ctx, p.Plan.Workspace)
	if candidateErr == nil && candidate == p.Plan.Candidate {
		observation.Candidate = &candidate
	}
	outcome := "UNKNOWN"
	if observation.Remote != nil && remote.Commit != nil && *remote.Commit == p.Plan.Candidate.Head && observation.Candidate != nil {
		outcome = "CONFIRMED"
	} else {
		observation.Error = "push final state could not be verified"
	}
	hash, err := canonical.Hash("harness.push-observation.v1", observation)
	if err != nil {
		return s, err
	}
	id, err := p.Intent.ID()
	if err != nil {
		return s, err
	}
	appendErr := Append(path, "push.observed", PushReceipt{effects.Receipt{Version: 1, IntentID: id, Outcome: outcome, ObservationHash: hash}, observation})
	latest, readErr := Inspect(path)
	if outcome == "UNKNOWN" {
		return latest, errors.Join(errors.New(observation.Error), remoteErr, candidateErr, appendErr, readErr)
	}
	return latest, errors.Join(appendErr, readErr)
}
