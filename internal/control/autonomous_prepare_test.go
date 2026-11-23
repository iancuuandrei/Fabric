package control

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/agenttree"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
)

func TestPrepareAutonomousRegistersOptInExplorerRootWithoutDispatch(t *testing.T) {
	c := graphCreation(t, 1)
	c.Config = modelAccessSnapshot(t, "subscription").Creation.Config
	c.Config.PlannerContract = plannerContractGraphV1
	c.Config.ExplorerContract = "json-v2"
	c.Execution.ScheduledExplorerDispatchVersion = 1
	path, _ := graphAwaitingApproval(t, c)
	prepared, err := PrepareAutonomous(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := agenttree.Inspect(path + ".agent-tree")
	if err != nil || tree.TreeID != prepared.RunID || len(tree.Nodes) != 1 {
		t.Fatal("prepared explorer root missing", err)
	}
	resultHash, err := canonical.Hash("harness.planner-result.v1", *prepared.Plan)
	if err != nil || tree.Nodes[0].Role != "planner" || tree.Nodes[0].Authority != agenttree.AuthorityReadOnly || tree.Nodes[0].Status != agenttree.StatusSucceeded || tree.Nodes[0].ResultSHA256 != resultHash {
		t.Fatal("root is not the exact accepted planner result", err)
	}
	if len(prepared.Explorations) != 0 || len(prepared.ModelAccess) != 0 {
		t.Fatal("root preparation dispatched role work")
	}
	if err := prepareManagedExplorerRoot(path, prepared); err != nil {
		t.Fatal(err)
	}
	again, err := agenttree.Inspect(path + ".agent-tree")
	if err != nil || !sameCanonical(tree, again) {
		t.Fatal("root preparation changed existing topology", err)
	}
	planner, err := plannerInvocationForSnapshot(prepared)
	if err != nil {
		t.Fatal(err)
	}
	contextHash, err := access.InputID(planner.Input)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []agenttree.Status{agenttree.StatusRunning, agenttree.StatusUnknown, agenttree.StatusSucceeded} {
		t.Run(string(status), func(t *testing.T) {
			otherPath := filepath.Join(t.TempDir(), "run.jsonl")
			node, err := agenttree.Create(otherPath+".agent-tree", prepared.RunID, agenttree.NodeSpec{Name: "root", Role: "planner", Authority: agenttree.AuthorityReadOnly, InvocationID: planner.ID, ContextSHA256: contextHash})
			if err != nil {
				t.Fatal(err)
			}
			if err := agenttree.ObserveStatus(otherPath+".agent-tree", node.AgentID, agenttree.StatusRunning); err != nil {
				t.Fatal(err)
			}
			if status == agenttree.StatusUnknown {
				if err := agenttree.ObserveStatus(otherPath+".agent-tree", node.AgentID, status); err != nil {
					t.Fatal(err)
				}
			} else if status == agenttree.StatusSucceeded {
				if err := agenttree.ObserveResult(otherPath+".agent-tree", node.AgentID, strings.Repeat("f", 64)); err != nil {
					t.Fatal(err)
				}
			}
			before, err := agenttree.Inspect(otherPath + ".agent-tree")
			if err != nil {
				t.Fatal(err)
			}
			if err := prepareManagedExplorerRoot(otherPath, prepared); err == nil {
				t.Fatal("nonterminal or foreign-result root admitted")
			}
			after, err := agenttree.Inspect(otherPath + ".agent-tree")
			if err != nil || !sameCanonical(before, after) {
				t.Fatal("rejection mutated existing root", err)
			}
		})
	}
}

func TestPrepareAutonomousStopsBeforeRolesAndResumeKeepsOwnership(t *testing.T) {
	c := graphCreation(t, 3)
	// The resumed run must stop at the missing writer route, after its fake,
	// read-only explorers have exercised the normal graph continuation path.
	c.Config.Writer = nil
	path, planned := graphAwaitingApproval(t, c)
	prepared, err := PrepareAutonomous(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.State != "IMPLEMENTING" || prepared.Workspace == nil || prepared.Candidate == nil || prepared.WorkspaceOutcome != "CONFIRMED" {
		t.Fatalf("prepare-only did not stop on a confirmed implementation workspace: %#v", prepared)
	}
	if prepared.MachineApproval == nil || prepared.MachineApproval.Actor != "fabric:autonomous" || prepared.MachineApproval.PlanID != planned.PlanID {
		t.Fatal("prepare-only did not machine-authorize the exact accepted plan")
	}
	if prepared.Graph == nil || prepared.Graph.PlanID != prepared.PlanID || prepared.Graph.Revision != 1 || len(prepared.Graph.Evidence) != 0 {
		t.Fatalf("prepare-only did not persist the accepted graph without task progress: %#v", prepared.Graph)
	}
	if len(prepared.Explorations) != 0 || len(prepared.ExplorerRuns) != 0 || prepared.ExplorerHost != nil || prepared.WriterHost != nil || prepared.WriterProposal != nil || prepared.FileIntent != nil || prepared.Verification != nil {
		t.Fatal("prepare-only crossed the explorer/writer boundary")
	}
	if len(prepared.TaskContexts) != 0 {
		t.Fatal("prepare-only admitted role context before dispatch")
	}
	candidate, err := prepared.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	planID, graphDigest := prepared.PlanID, prepared.Graph.Digest

	// Candidate overlay capture is now available to the lexical CLI. This call
	// only produces the exact preview; it does not materialize an RI artifact.
	overlayPreview, err := PrepareLexicalOverlay(context.Background(), path, filepath.Join(t.TempDir(), "candidate-overlay"))
	if err != nil {
		t.Fatal(err)
	}
	if overlayPreview.Plan.CandidateID != candidate {
		t.Fatal("lexical overlay preview did not bind the prepared candidate")
	}

	// Normal resume consumes the existing accepted plan and workspace. Fake
	// explorers are local deterministic fixtures; no provider is called. The
	// missing writer route supplies a finite expected stop after exploration.
	_, err = RunAutonomous(context.Background(), path)
	if err == nil {
		t.Fatalf("resume did not stop at the unconfigured writer route: %v", err)
	}
	resumed, inspectErr := Inspect(path)
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	resumedCandidate, err := resumed.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	if resumed.RunID != prepared.RunID || resumed.PlanID != planID || resumed.Graph == nil || resumed.Graph.Digest != graphDigest || resumedCandidate != candidate {
		t.Fatal("resume changed the prepared run, plan, graph, or candidate ownership")
	}
	if resumed.WorkspaceOutcome != "CONFIRMED" || len(resumed.Explorations) == 0 || resumed.WriterProposal != nil || resumed.FileIntent != nil {
		t.Fatalf("unexpected resume boundary: state=%s explorers=%d writer=%v file=%v", resumed.State, len(resumed.Explorations), resumed.WriterProposal != nil, resumed.FileIntent != nil)
	}
	for _, task := range resumed.Graph.Graph.Tasks {
		if task.Kind == engineeringplan.Implementation && len(task.Attempts) != 0 {
			t.Fatal("resume recorded implementation progress without an actual writer receipt")
		}
	}
}

func TestPrepareAutonomousRejectsLegacyRun(t *testing.T) {
	path, _ := autonomousAwaitingApproval(t, autonomousCreation(t, 1))
	if _, err := PrepareAutonomous(context.Background(), path); err == nil || !strings.Contains(err.Error(), "graph-enabled") {
		t.Fatalf("prepare-only accepted a legacy autonomous run: %v", err)
	}
}

func TestPrepareAutonomousRejectsPreviouslyAdmittedTaskContext(t *testing.T) {
	path, _ := graphAwaitingApproval(t, graphCreation(t, 3))
	prepared, err := PrepareAutonomous(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	var research *engineeringplan.Task
	for i := range prepared.Graph.Graph.Tasks {
		if prepared.Graph.Graph.Tasks[i].Kind == engineeringplan.Research {
			research = &prepared.Graph.Graph.Tasks[i]
			break
		}
	}
	if research == nil {
		t.Fatal("fixture graph has no research task")
	}
	question, err := explorerQuestionForTask(prepared, *research)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitTaskContext(context.Background(), path, "explorer", question); err != nil {
		t.Fatal(err)
	}
	withContext, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(withContext.TaskContexts) != 1 || len(withContext.ExplorerRuns) != 0 || len(withContext.Explorations) != 0 {
		t.Fatalf("expected context admission without explorer dispatch: contexts=%d runs=%d explorations=%d", len(withContext.TaskContexts), len(withContext.ExplorerRuns), len(withContext.Explorations))
	}
	if _, err := PrepareAutonomous(context.Background(), path); err == nil || !strings.Contains(err.Error(), "task context was admitted") {
		t.Fatalf("prepare-only accepted a run after real task-context admission: %v", err)
	}
}
