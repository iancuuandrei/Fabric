package gitlocal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/worktree"
)

func gitObjectCommand(ctx context.Context, root string, input []byte, limit int, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	argv := []string{"--no-optional-locks", "--no-replace-objects", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-C", root}
	cmd := exec.CommandContext(ctx, "git", append(argv, args...)...)
	cmd.WaitDelay = time.Second
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = bytes.NewReader(input)
	out := &boundedBuffer{remaining: limit}
	stderr := &boundedBuffer{remaining: 64 << 10}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("Git object operation: %w: %s", err, stderr.String())
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out.Bytes(), nil
}

// StoreObjects writes and reads back an exact authorized object set, without
// moving refs or changing the index. The caller must hold the workspace lease
// and have persisted intent before calling. An error can leave partial objects;
// it is not proof of NOT_APPLIED and does not authorize automatic retry.
func StoreObjects(ctx context.Context, plan CommitPlan, objects CandidateObjects, intent effects.Intent, authorization effects.Authorization) error {
	if err := validateAuthorization(plan, intent, authorization); err != nil {
		return err
	}
	return storeObjects(ctx, plan, objects)
}

func validateAuthorization(plan CommitPlan, intent effects.Intent, authorization effects.Authorization) error {
	id, err := plan.ID()
	if err != nil {
		return err
	}
	repositoryID, err := plan.Workspace.Request.Source.ID()
	if err != nil {
		return err
	}
	if intent.Kind != "commit" || intent.InputHash != id || intent.RunID != plan.Workspace.Request.RunID || intent.RepositoryID != repositoryID {
		return errors.New("commit storage intent mismatch")
	}
	if err := authorization.Validate(intent); err != nil {
		return err
	}
	return nil
}

func storeObjects(ctx context.Context, plan CommitPlan, objects CandidateObjects) error {
	if err := objects.Validate(plan); err != nil {
		return err
	}
	before, err := worktree.Fingerprint(ctx, plan.Workspace)
	if err != nil {
		return err
	}
	if before != plan.Candidate {
		return errors.New("candidate changed before object storage")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	store := func(kind, id string, data []byte) error {
		out, err := gitObjectCommand(ctx, plan.Workspace.Request.Path, data, 128, "hash-object", "-w", "-t", kind, "--stdin")
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(out)) != id {
			return errors.New("stored Git object ID mismatch")
		}
		out, err = gitObjectCommand(ctx, plan.Workspace.Request.Path, nil, len(data)+1, "cat-file", kind, id)
		if err != nil {
			return err
		}
		if !bytes.Equal(out, data) {
			return errors.New("stored Git object bytes mismatch")
		}
		return nil
	}
	seen := map[string]bool{}
	for _, blob := range objects.Blobs {
		if seen[blob.ObjectID] {
			continue
		}
		if err := store("blob", blob.ObjectID, blob.Data); err != nil {
			return err
		}
		seen[blob.ObjectID] = true
	}
	for _, tree := range objects.Trees {
		if seen[tree.ObjectID] {
			continue
		}
		if err := store("tree", tree.ObjectID, tree.Data); err != nil {
			return err
		}
		seen[tree.ObjectID] = true
	}
	if err := store("commit", objects.CommitID, objects.CommitData); err != nil {
		return err
	}
	after, err := worktree.Fingerprint(ctx, plan.Workspace)
	if err != nil {
		return err
	}
	if after != plan.Candidate {
		return errors.New("candidate changed during object storage")
	}
	return nil
}
