package control

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/iancuuandrei/fabric/internal/acpruntime"
	"github.com/iancuuandrei/fabric/internal/clauderuntime"
	"github.com/iancuuandrei/fabric/internal/directapi"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

// isExternalRuntime reports whether runtimeID uses Fabric's unified external
// runtime dispatcher (Claude Code CLI, ACP stdio, or Direct HTTP API).
func isExternalRuntime(runtimeID string) bool {
	switch runtimeID {
	case clauderuntime.RuntimeName, acpruntime.RuntimeName, directapi.RuntimeName:
		return true
	default:
		return false
	}
}

// executeExternalRole dispatches one bounded invocation to claude-cli,
// acp-stdio, or direct-api under a per-invocation durable runtime journal.
func executeExternalRole(ctx context.Context, controllerPath string, s Snapshot, inv runtime.Invocation) (runtime.Result, error) {
	workDir := s.Creation.Repository.Root
	if s.Workspace != nil && s.Workspace.Request.Path != "" {
		workDir = s.Workspace.Request.Path
	}
	stem := inv.Profile.Role + ".invocation-" + inv.ID

	switch inv.Profile.Runtime {
	case clauderuntime.RuntimeName:
		if s.Creation.Config.Claude == nil {
			return runtime.Result{}, errors.New("claude configuration required for claude-cli runtime")
		}
		c := s.Creation.Config.Claude
		res, _, err := clauderuntime.Execute(ctx, clauderuntime.Config{
			Executable:     c.Executable,
			ExecutableHash: c.ExecutableHash,
			WorkDir:        workDir,
			JournalPath:    controllerPath + "." + stem + ".claude-runtime.jsonl",
			Timeout:        time.Duration(c.TimeoutSeconds) * time.Second,
			ExtraArgs:      c.ExtraArgs,
		}, inv)
		return res, err

	case acpruntime.RuntimeName:
		if s.Creation.Config.ACP == nil {
			return runtime.Result{}, errors.New("acp configuration required for acp-stdio runtime")
		}
		a := s.Creation.Config.ACP
		res, _, err := acpruntime.Execute(ctx, acpruntime.Config{
			Executable:     a.Executable,
			ExecutableHash: a.ExecutableHash,
			Args:           a.Args,
			WorkDir:        workDir,
			JournalPath:    controllerPath + "." + stem + ".acp-runtime.jsonl",
			Timeout:        time.Duration(a.TimeoutSeconds) * time.Second,
		}, inv)
		return res, err

	case directapi.RuntimeName:
		if s.Creation.Config.DirectAPI == nil {
			return runtime.Result{}, errors.New("direct_api configuration required for direct-api runtime")
		}
		d := s.Creation.Config.DirectAPI
		res, _, err := directapi.Execute(ctx, directapi.Config{
			Dialect:         d.Dialect,
			BaseURL:         d.BaseURL,
			APIKeyEnv:       d.APIKeyEnv,
			WorkDir:         workDir,
			MaxOutputTokens: d.MaxOutputTokens,
			Timeout:         time.Duration(d.TimeoutSeconds) * time.Second,
			JournalPath:     controllerPath + "." + stem + ".directapi-runtime.jsonl",
		}, inv)
		return res, err

	default:
		return runtime.Result{}, fmt.Errorf("unsupported external runtime %q", inv.Profile.Runtime)
	}
}

// executeDirectProviderRole dispatches a non-planner role (explorer, writer,
// fixer, or reviewer) through the existing durable provider-api runtime.
func executeDirectProviderRole(ctx context.Context, controllerPath string, s Snapshot, inv runtime.Invocation, question string) (runtime.Result, error) {
	if receipt, ok := s.ProviderRuntime[inv.ID]; ok {
		if receipt.Role != inv.Profile.Role {
			return runtime.Result{}, errors.New("recorded direct provider role receipt mismatch")
		}
		return receipt.Result, nil
	}
	res, receipt, err := executeDirectProviderRuntime(ctx, controllerPath, s, inv)
	if err != nil {
		return runtime.Result{}, err
	}
	receipt.Question = question
	if err := Append(controllerPath, "role.provider-observed", receipt); err != nil {
		return runtime.Result{}, err
	}
	if err := settleProviderDispatchTaskPool(controllerPath, s.Creation.Config, s.RunID, inv, receipt); err != nil {
		return runtime.Result{}, err
	}
	return res, nil
}
