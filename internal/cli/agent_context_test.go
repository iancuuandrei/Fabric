package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/control"
)

func TestAgentContextCLIActivationAndOptOut(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		root := autonomousCLIFixture(t)
		if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("retained fixture rules"), 0600); err != nil {
			t.Fatal(err)
		}
		cliGit(t, root, "add", "AGENTS.md")
		cliGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "guidance")
		args := []string{"run", "--autonomous", "--prepare-only", "--max-parallel", "1"}
		if !enabled {
			args = append(args, "--agent-context=false")
		}
		args = append(args, "A bounded fixture objective")
		var out bytes.Buffer
		// The stock fake deliberately produces prose, not a valid coding graph.
		// Verify retained inputs at its explicit rejection boundary, not READY.
		err := Execute(context.Background(), args, root, &out)
		if err == nil || !strings.Contains(err.Error(), "semantic_correction_budget_exhausted") {
			t.Fatal("fake planner did not retain its expected rejection", err)
		}
		raw := out.Bytes()
		var result struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		s, err := control.Inspect(filepath.Join(root, ".harness", "runs", result.RunID+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if (s.Creation.AgentContext != nil) != enabled {
			t.Fatal("activation differs from requested option")
		}
		if enabled && (len(s.Creation.AgentContext.Instructions) != 1 || s.Creation.AgentContext.Instructions[0].Content != "retained fixture rules") {
			t.Fatal("committed guidance missing")
		}
		planArgs := []string{"run", "--autonomous", "--inspect-plan"}
		if !enabled {
			planArgs = append(planArgs, "--agent-context=false")
		}
		var plan struct {
			ExecutionPlan struct {
				AgentContext struct {
					Enabled bool `json:"enabled"`
				} `json:"agent_context"`
			} `json:"execution_plan"`
		}
		if err = json.Unmarshal(mustExecuteCLI(t, root, planArgs...), &plan); err != nil || plan.ExecutionPlan.AgentContext.Enabled != enabled {
			t.Fatal("inspection differs from creation", err)
		}
	}
}
