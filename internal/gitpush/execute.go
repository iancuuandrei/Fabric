package gitpush

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/worktree"
)

func gitInTemporary(ctx context.Context, directory, objects string, args ...string) error {
	return gitInTemporaryCredential(ctx, directory, objects, nil, args...)
}

func gitInTemporaryCredential(ctx context.Context, directory, objects string, credential *Credential, args ...string) error {
	base := []string{"--no-optional-locks", "--no-replace-objects", "-c", "protocol.allow=never", "-c", "protocol.file.allow=always", "-c", "protocol.https.allow=always", "-c", "http.followRedirects=false", "-c", "credential.helper=", "-c", "core.askPass=", "-c", "core.hooksPath=" + os.DevNull}
	command := exec.CommandContext(ctx, "git", append(base, args...)...)
	command.Dir = directory
	command.Env = credential.environment(isolatedEnvironment(directory))
	if objects != "" {
		command.Env = append(command.Env, "GIT_OBJECT_DIRECTORY="+objects)
	}
	command.WaitDelay = time.Second
	stdout, stderr := &capture{limit: 4096}, &capture{limit: 4096}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		return errors.Join(errors.New("isolated Git push operation failed"), ctx.Err())
	}
	if stdout.overflow || stderr.overflow {
		return errors.New("isolated Git push output bound exceeded")
	}
	return nil
}

func removeTemporaryRepository(directory string) error {
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return err
	}
	if filepath.Dir(resolved) != filepath.Clean(parent) || !strings.HasPrefix(filepath.Base(resolved), "engorch-push-") {
		return errors.New("temporary Git cleanup path escaped its parent")
	}
	return os.RemoveAll(resolved)
}

func sameCommit(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// Execute pushes one exact committed candidate after approval and preflight.
// The caller must hold the workspace lease and persist this intent before calling.
// A separate ancestry check prohibits history replacement even with an exact
// lease. Errors may follow remote mutation and never authorize automatic retry.
// The returned observation, when present, is independently read remote state.
func Execute(ctx context.Context, plan Plan, intent effects.Intent, authorization effects.Authorization) (observation RefObservation, err error) {
	return execute(ctx, plan, intent, authorization, nil)
}

// ExecuteAuthenticated uses explicit destination-bound authentication for the
// push and both remote observations. Effect approval and lease rules are unchanged.
func ExecuteAuthenticated(ctx context.Context, plan Plan, intent effects.Intent, authorization effects.Authorization, credential *Credential) (RefObservation, error) {
	if credential == nil {
		return RefObservation{}, errors.New("explicit GitHub credential required")
	}
	return execute(ctx, plan, intent, authorization, credential)
}

func execute(ctx context.Context, plan Plan, intent effects.Intent, authorization effects.Authorization, credential *Credential) (observation RefObservation, err error) {
	if err := credential.validate(plan.Destination); err != nil {
		return observation, err
	}
	expected, err := plan.Intent(intent.PlanID)
	if err != nil {
		return observation, err
	}
	if expected != intent {
		return observation, errors.New("push intent payload substitution")
	}
	if err := authorization.Validate(intent); err != nil {
		return observation, err
	}
	before, err := worktree.Pristine(ctx, plan.Workspace)
	if err != nil || before != plan.Candidate {
		return observation, errors.Join(errors.New("push requires the exact committed candidate"), err)
	}
	remote, err := observeRemote(ctx, plan, credential)
	if err != nil {
		return observation, err
	}
	if !sameCommit(remote.Commit, plan.ExpectedOld) {
		return observation, errors.New("remote branch differs from approved expected state")
	}
	directory, err := os.MkdirTemp("", "engorch-push-")
	if err != nil {
		return observation, err
	}
	defer func() { err = errors.Join(err, removeTemporaryRepository(directory)) }()
	bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := gitInTemporary(bounded, directory, "", "init", "--bare", "--template=", "--object-format="+plan.Workspace.Request.Source.ObjectFormat, "-q"); err != nil {
		return observation, err
	}
	objects := filepath.Join(plan.Workspace.Request.Source.CommonDir, "objects")
	if plan.ExpectedOld != nil {
		if err := gitInTemporary(bounded, directory, objects, "merge-base", "--is-ancestor", *plan.ExpectedOld, plan.Candidate.Head); err != nil {
			return observation, errors.Join(errors.New("approved remote commit is not a known ancestor"), err)
		}
	}
	old := ""
	if plan.ExpectedOld != nil {
		old = *plan.ExpectedOld
	}
	pushErr := gitInTemporaryCredential(bounded, directory, objects, credential, "push", "--porcelain", "--no-follow-tags", "--recurse-submodules=no", "--force-with-lease="+plan.TargetRef+":"+old, "--", plan.Destination, plan.Candidate.Head+":"+plan.TargetRef)
	// A lost command response is distinct from the remote observation. Readback
	// has its own bounded context and remains useful after caller cancellation.
	observation, observeErr := observeRemote(context.WithoutCancel(ctx), plan, credential)
	afterCtx, afterCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer afterCancel()
	after, sourceErr := worktree.Fingerprint(afterCtx, plan.Workspace)
	if sourceErr == nil && after != before {
		sourceErr = errors.New("source candidate changed during push")
	}
	return observation, errors.Join(pushErr, observeErr, sourceErr)
}
