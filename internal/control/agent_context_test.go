package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/agentcontext"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
)

func agentContextCreation(t *testing.T) Creation {
	t.Helper()
	c := graphCreation(t, 1)
	c.Config.PlannerContract = plannerContractGraphV2
	id, err := c.Repository.ID()
	if err != nil {
		t.Fatal(err)
	}
	text := "retained repository rules"
	hash := sha256.Sum256([]byte(text))
	c.AgentContext = &agentcontext.Bundle{Version: 1, SourceID: id, SourceCommit: c.Repository.Commit, Instructions: []agentcontext.Document{{Path: "AGENTS.md", SHA256: hex.EncodeToString(hash[:]), Content: text}}, Skills: []agentcontext.Skill{}}
	return c
}

func TestAgentContextPlannerAndCreationReplay(t *testing.T) {
	c := agentContextCreation(t)
	p := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(p, "run.created", c); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	first, err := plannerInvocationForSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Input, "retained repository rules") || !strings.Contains(first.Input, "\"skills\"") {
		t.Fatal("planner missing bound instructions or skill schema")
	}
	if err = os.WriteFile(filepath.Join(c.Repository.Root, "AGENTS.md"), []byte("changed live instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	second, err := plannerInvocationForSnapshot(s)
	if err != nil || first != second {
		t.Fatal("replay invocation drift", err)
	}
	legacy := c
	legacy.AgentContext = nil
	old, err := plannerInvocationForSnapshot(Snapshot{Creation: legacy})
	if err != nil {
		t.Fatal(err)
	}
	expected, err := plannerInvocationWithContextsAndRecipe(c.Config, c.Objective, nil, nil, c.Execution)
	if err != nil || old != expected {
		t.Fatal("legacy invocation changed", err)
	}
	if old.ID == first.ID {
		t.Fatal("guidance not bound to invocation identity")
	}
	bad := c
	copy := *c.AgentContext
	bad.AgentContext = &copy
	bad.AgentContext.SourceCommit = strings.Repeat("c", 40)
	if err = Append(filepath.Join(t.TempDir(), "invalid.jsonl"), "run.created", bad); err == nil {
		t.Fatal("source substitution admitted")
	}
}

func TestAgentContextPlannerUsesSharedWireSchema(t *testing.T) {
	i, err := plannerInvocationForSnapshot(Snapshot{Creation: agentContextCreation(t)})
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		OutputSchema json.RawMessage `json:"output_schema"`
	}
	if err := json.Unmarshal([]byte(i.Input), &envelope); err != nil {
		t.Fatal(err)
	}
	expected, err := engineeringplan.AgentContextPlannerJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	if string(envelope.OutputSchema) != string(expected) {
		t.Fatal("controller schema differs from the admitted wire schema")
	}
}

func TestAgentContextRolePromptAndGraphValidation(t *testing.T) {
	c := agentContextCreation(t)
	s := Snapshot{Creation: c}
	task := engineeringplan.Task{ID: "implementation", Kind: engineeringplan.Implementation, ScopePaths: []string{"internal"}, WritePaths: []string{"internal/x.go"}}
	value := struct {
		Instruction string `json:"instruction"`
		CandidateID string `json:"candidate_id"`
	}{"unchanged role contract", strings.Repeat("a", 64)}
	for _, role := range []string{"writer", "fixer", "explorer", "reviewer"} {
		raw, err := agentContextPromptBytes(s, role, &task, value)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Context     agentcontext.Selection `json:"agent_context"`
			CandidateID string                 `json:"candidate_id"`
		}
		if err = json.Unmarshal(raw, &got); err != nil || got.Context.Role != role || got.CandidateID != value.CandidateID {
			t.Fatal("role or candidate substitution", err)
		}
	}
	legacy := s
	legacy.Creation.AgentContext = nil
	raw, err := agentContextPromptBytes(legacy, "writer", &task, value)
	expected, _ := canonical.Bytes(value)
	if err != nil || string(raw) != string(expected) {
		t.Fatal("legacy role prompt changed", err)
	}
	g := validGraphFixture()
	g.Tasks[0].Skills = []string{"missing"}
	if validateAgentContextGraph(s, g) == nil || validateAgentContextGraph(legacy, g) == nil {
		t.Fatal("unavailable task skills admitted")
	}
}
