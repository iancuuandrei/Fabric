package hooks

import (
	"context"
	"runtime"
	"testing"
)

func TestHookValidationAndExecution(t *testing.T) {
	if err := (Hook{Name: "bad", Event: "unknown", Argv: []string{"echo"}}).Validate(); err == nil {
		t.Fatal("expected unknown event error")
	}

	okArgv := []string{"git", "--version"}
	failArgv := []string{"git", "rev-parse", "--verify", "refs/does-not-exist-fabric-test"}
	if runtime.GOOS == "windows" {
		okArgv = []string{"git", "--version"}
	}

	hooks := []Hook{
		{Name: "ok-hook", Event: EventPreVerify, Argv: okArgv, TimeoutSeconds: 10},
		{Name: "warn-hook", Event: EventPreVerify, Argv: failArgv, OnFailure: FailureWarn, TimeoutSeconds: 10},
		{Name: "block-hook", Event: EventPreVerify, Argv: failArgv, OnFailure: FailureBlock, TimeoutSeconds: 10},
		{Name: "never-runs", Event: EventPreVerify, Argv: okArgv, TimeoutSeconds: 10},
	}

	results, err := RunEvent(context.Background(), hooks, EventPreVerify, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected blocking hook to return an error")
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 executed hooks before block, got %d", len(results))
	}
	if !results[0].Passed || results[0].Blocked {
		t.Fatalf("expected ok-hook to pass: %+v", results[0])
	}
	if results[1].Passed || results[1].Blocked {
		t.Fatalf("expected warn-hook to fail without blocking: %+v", results[1])
	}
	if results[2].Passed || !results[2].Blocked {
		t.Fatalf("expected block-hook to fail and block: %+v", results[2])
	}
}
