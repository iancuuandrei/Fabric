package control

import (
	"context"
	"testing"

	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/hooks"
	"github.com/iancuuandrei/fabric/internal/repository"
)

func TestRunLifecycleHooksBlocksAndWarns(t *testing.T) {
	root := t.TempDir()
	snap := Snapshot{
		RunID: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		State: "VERIFYING",
		Creation: Creation{
			Repository: repository.Identity{Root: root},
			Config: config.Config{
				Hooks: []hooks.Hook{
					{Name: "ok-pre-verify", Event: hooks.EventPreVerify, Argv: []string{"git", "--version"}, TimeoutSeconds: 10},
					{Name: "warn-pre-verify", Event: hooks.EventPreVerify, Argv: []string{"git", "rev-parse", "--verify", "refs/missing-fabric-hook-test"}, OnFailure: hooks.FailureWarn, TimeoutSeconds: 10},
				},
			},
		},
	}
	if err := runLifecycleHooks(context.Background(), snap, hooks.EventPreVerify); err != nil {
		t.Fatalf("expected warn hook not to block: %v", err)
	}

	snap.Creation.Config.Hooks = append(snap.Creation.Config.Hooks, hooks.Hook{
		Name:           "block-pre-verify",
		Event:          hooks.EventPreVerify,
		Argv:           []string{"git", "rev-parse", "--verify", "refs/missing-fabric-hook-test"},
		OnFailure:      hooks.FailureBlock,
		TimeoutSeconds: 10,
	})
	if err := runLifecycleHooks(context.Background(), snap, hooks.EventPreVerify); err == nil {
		t.Fatal("expected blocking pre_verify hook to return error")
	}
}
