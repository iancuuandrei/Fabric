package control

import (
	"context"
	"os"

	"github.com/iancuuandrei/fabric/internal/hooks"
)

// runLifecycleHooks executes configured deterministic hooks for one lifecycle
// event inside the active candidate workspace (falling back to the repository
// root when no worktree exists yet). Hooks receive bounded metadata env vars
// and never bypass Fabric's authority or journal gates.
func runLifecycleHooks(ctx context.Context, s Snapshot, event string) error {
	if len(s.Creation.Config.Hooks) == 0 {
		return nil
	}
	workDir := s.Creation.Repository.Root
	if s.Workspace != nil && s.Workspace.Request.Path != "" {
		workDir = s.Workspace.Request.Path
	}
	env := append(os.Environ(),
		"FABRIC_RUN_ID="+s.RunID,
		"FABRIC_RUN_STATE="+s.State,
		"FABRIC_HOOK_EVENT="+event,
	)
	_, err := hooks.RunEvent(ctx, s.Creation.Config.Hooks, event, workDir, env)
	return err
}
