package control

import (
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskscheduler"
)

func TestAgentDispatchOrdersAdmissionAndExactResultAroundLifecycle(t *testing.T) {
	controllerPath, snapshot, planner := agentDispatchFixture(t)
	binding, err := beginAgentDispatch(controllerPath, snapshot, planner)
	if err != nil {
		t.Fatal(err)
	}
	if err := markAgentDispatchRunning(&binding); err != nil {
		t.Fatal(err)
	}
	admitted, err := Inspect(controllerPath)
	if err != nil || admitted.AgentDispatch[planner.ID].Observation != nil {
		t.Fatalf("admitted state = %+v: %v", admitted.AgentDispatch, err)
	}
	if unresolved := lifecycleUnresolved(admitted); !containsString(unresolved, "agent-dispatch:"+planner.ID) {
		t.Fatalf("unresolved = %v", unresolved)
	}
	if _, err := RequestPause(controllerPath, "operator", "pause-before-child"); err != nil {
		t.Fatal(err)
	}
	resultHash := strings.Repeat("f", 64)
	if err := finishAgentDispatch(binding, resultHash); err != nil {
		t.Fatal(err)
	}
	finished, err := Inspect(controllerPath)
	if err != nil || finished.AgentDispatch[planner.ID].Observation == nil || finished.AgentDispatch[planner.ID].Observation.ResultSHA256 != resultHash {
		t.Fatalf("finished state = %+v: %v", finished.AgentDispatch, err)
	}

	childProfile := runtime.Profile{Runtime: "opencode-http", Provider: "engorch-openai", Model: "gpt-5.2-codex", Effort: "high", Role: "explorer"}
	child, err := runtime.NewInvocation(childProfile, "Inspect the candidate.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := beginAgentDispatch(controllerPath, finished, child); err == nil {
		t.Fatal("pause did not win controller-ordered child admission")
	}
	tree, err := agenttree.Inspect(controllerPath + ".agent-tree")
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range tree.Nodes {
		if node.InvocationID == child.ID && node.Status != agenttree.StatusQueued {
			t.Fatalf("unadmitted child status = %s", node.Status)
		}
	}
}

func TestAgentDispatchUnknownCanReconcileWithoutNewAdmission(t *testing.T) {
	controllerPath, snapshot, planner := agentDispatchFixture(t)
	binding, err := beginAgentDispatch(controllerPath, snapshot, planner)
	if err != nil {
		t.Fatal(err)
	}
	if err := markAgentDispatchRunning(&binding); err != nil {
		t.Fatal(err)
	}
	if err := finishAgentDispatchUnknown(binding); err != nil {
		t.Fatal(err)
	}
	recovered, err := beginAgentDispatch(controllerPath, snapshot, planner)
	if err != nil {
		t.Fatal(err)
	}
	resultHash := strings.Repeat("e", 64)
	if err := finishAgentDispatch(recovered, resultHash); err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := state.AgentDispatch[planner.ID]
	if dispatch.Observation == nil || dispatch.Observation.Status != agenttree.StatusSucceeded || dispatch.Observation.ResultSHA256 != resultHash {
		t.Fatalf("reconciled dispatch = %+v", dispatch)
	}
	tree, err := agenttree.Inspect(controllerPath + ".agent-tree")
	if err != nil || len(tree.Nodes) != 1 || tree.Nodes[0].Status != agenttree.StatusSucceeded || tree.Nodes[0].ResultSHA256 != resultHash {
		t.Fatalf("reconciled tree = %+v: %v", tree, err)
	}
}

func TestAgentDispatchBindsReadOnlyAndScopedWriterChildren(t *testing.T) {
	controllerPath, snapshot, planner := agentDispatchFixture(t)
	root, err := beginAgentDispatch(controllerPath, snapshot, planner)
	if err != nil {
		t.Fatal(err)
	}
	if err := markAgentDispatchRunning(&root); err != nil {
		t.Fatal(err)
	}
	if err := finishAgentDispatch(root, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		role      string
		authority agenttree.Authority
		result    string
	}{
		{"reviewer", agenttree.AuthorityReadOnly, strings.Repeat("c", 64)},
		{"writer", agenttree.AuthorityScopedWriter, strings.Repeat("b", 64)},
	} {
		profile := runtime.Profile{Runtime: "opencode-http", Provider: "engorch-openai", Model: "gpt-5.2-codex", Effort: "high", Role: item.role}
		invocation, err := runtime.NewInvocation(profile, "Perform the admitted role.")
		if err != nil {
			t.Fatal(err)
		}
		binding, err := beginAgentDispatch(controllerPath, snapshot, invocation)
		if err != nil {
			t.Fatal(err)
		}
		if err := markAgentDispatchRunning(&binding); err != nil {
			t.Fatal(err)
		}
		if binding.Node.ParentAgentID != root.Node.AgentID || binding.Node.Authority != item.authority {
			t.Fatalf("%s binding = %+v", item.role, binding.Node)
		}
		if err := finishAgentDispatch(binding, item.result); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenCodeChildBootstrapsCompletedNonOpenCodePlannerRoot(t *testing.T) {
	controllerPath, snapshot, _ := agentDispatchFixture(t)
	snapshot.Creation.Config.Planner = runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "planner-v1", Effort: "none", Role: "planner"}
	planner, err := runtime.NewInvocation(snapshot.Creation.Config.Planner, snapshot.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	model, provider, effort := planner.Profile.Model, planner.Profile.Provider, planner.Profile.Effort
	plannerResult := runtime.Result{Version: 1, InvocationID: planner.ID, Requested: planner.Profile, ObservedModel: &model, ObservedProvider: &provider, ObservedEffort: &effort, Output: "Use the admitted candidate workflow."}
	snapshot.Plan = &plannerResult
	profile := runtime.Profile{Runtime: "opencode-http", Provider: "engorch-openai", Model: "gpt-5.2-codex", Effort: "high", Role: "writer"}
	writer, err := runtime.NewInvocation(profile, "Propose candidate changes.")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := beginAgentDispatch(controllerPath, snapshot, writer)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := agenttree.Inspect(controllerPath + ".agent-tree")
	if err != nil || len(tree.Nodes) != 2 {
		t.Fatalf("tree = %+v: %v", tree, err)
	}
	root, ok := agentNodeByID(tree, tree.RootAgentID)
	if !ok || root.InvocationID != planner.ID || root.Status != agenttree.StatusSucceeded || root.Authority != agenttree.AuthorityReadOnly || binding.Node.ParentAgentID != root.AgentID {
		t.Fatalf("root = %+v; child = %+v", root, binding.Node)
	}
}

func TestScheduledAgentTurnsUseExactPrecommittedExplorerNode(t *testing.T) {
	controllerPath, snapshot, planner := agentDispatchFixture(t)
	rootBinding, err := beginAgentDispatch(controllerPath, snapshot, planner)
	if err != nil {
		t.Fatal(err)
	}
	if err := markAgentDispatchRunning(&rootBinding); err != nil {
		t.Fatal(err)
	}
	if err := finishAgentDispatch(rootBinding, strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}
	profile := runtime.Profile{Runtime: "opencode-http", Provider: "engorch-openai", Model: "gpt-5.2-codex", Effort: "high", Role: "explorer"}
	first, err := runtime.NewInvocation(profile, "first scheduled explorer input")
	if err != nil {
		t.Fatal(err)
	}
	contextHash, err := access.InputID(first.Input)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := agenttree.ReserveChild(controllerPath+".agent-tree", snapshot.RunID, agenttree.NodeSpec{ParentAgentID: rootBinding.Node.AgentID, Name: "scheduled", Role: "explorer", Authority: agenttree.AuthorityReadOnly, InvocationID: first.ID, ContextSHA256: contextHash})
	if err != nil {
		t.Fatal(err)
	}
	child, err := agenttree.CommitChild(controllerPath+".agent-tree", reservation.ReservationID)
	if err != nil {
		t.Fatal(err)
	}
	initialTurn := &taskscheduler.AgentTurnBinding{ParentAgentID: rootBinding.Node.AgentID, AgentID: child.AgentID, TurnID: strings.Repeat("2", 64), TurnSequence: 1}
	binding, err := beginAgentDispatchForTurn(controllerPath, snapshot, first, initialTurn)
	if err != nil || binding.Node.AgentID != child.AgentID || binding.AgentTurn == nil || binding.AgentTurn.TurnID != initialTurn.TurnID {
		t.Fatal("initial scheduled turn", binding, err)
	}
	if err := markAgentDispatchRunning(&binding); err != nil {
		t.Fatal(err)
	}
	if err := finishAgentDispatch(binding, strings.Repeat("3", 64)); err != nil {
		t.Fatal(err)
	}
	second, err := runtime.NewInvocation(profile, "second scheduled explorer input")
	if err != nil {
		t.Fatal(err)
	}
	followUp := &taskscheduler.AgentTurnBinding{ParentAgentID: child.ParentAgentID, AgentID: child.AgentID, TurnID: strings.Repeat("4", 64), TurnSequence: 2}
	secondBinding, err := beginAgentDispatchForTurn(controllerPath, snapshot, second, followUp)
	if err != nil || secondBinding.InvocationID != second.ID {
		t.Fatal("follow-up scheduled turn", secondBinding, err)
	}
	if err := markAgentDispatchRunning(&secondBinding); err != nil {
		t.Fatal(err)
	}
	if err := finishAgentDispatch(secondBinding, strings.Repeat("5", 64)); err != nil {
		t.Fatal(err)
	}
	latest, err := Inspect(controllerPath)
	if err != nil || latest.AgentDispatch[first.ID].Observation == nil || latest.AgentDispatch[second.ID].Observation == nil {
		t.Fatal("turn observations", latest.AgentDispatch, err)
	}
	activity, err := agentcontrol.Inspect(controllerPath + ".agent-control")
	if err != nil || len(activity.Activities) != 4 || activity.Activities[0].TurnID != initialTurn.TurnID || activity.Activities[1].Status != agenttree.StatusSucceeded || activity.Activities[2].TurnID != followUp.TurnID || activity.Activities[3].Status != agenttree.StatusSucceeded {
		t.Fatal("agent control turn activity", activity.Activities, err)
	}
	wrong := *followUp
	wrong.AgentID = strings.Repeat("6", 64)
	if _, err := beginAgentDispatchForTurn(controllerPath, snapshot, second, &wrong); err == nil {
		t.Fatal("substituted scheduled agent admitted")
	}
}

func TestScheduledProviderJournalStemsSeparateTurns(t *testing.T) {
	first := &taskscheduler.AgentTurnBinding{ParentAgentID: strings.Repeat("1", 64), AgentID: strings.Repeat("2", 64), TurnID: strings.Repeat("3", 64), TurnSequence: 1}
	second := &taskscheduler.AgentTurnBinding{ParentAgentID: first.ParentAgentID, AgentID: first.AgentID, TurnID: strings.Repeat("4", 64), TurnSequence: 2}
	firstStem := providerRoleJournalStem("explorer", first)
	secondStem := providerRoleJournalStem("explorer", second)
	if firstStem == secondStem || firstStem != "explorer.turn-"+first.TurnID || secondStem != "explorer.turn-"+second.TurnID || providerRoleJournalStem("explorer", nil) != "explorer" {
		t.Fatal("scheduled runtime journal namespace collision", firstStem, secondStem)
	}
}

func agentDispatchFixture(t *testing.T) (string, Snapshot, runtime.Invocation) {
	return agentDispatchFixtureWithConfig(t, nil)
}

func agentDispatchFixtureWithConfig(t *testing.T, mutate func(*config.Config)) (string, Snapshot, runtime.Invocation) {
	t.Helper()
	controllerPath := filepath.Join(t.TempDir(), "run.db")
	cfg := providerRoutingConfig("opencode-http", "engorch-openai")
	cfg.Repository = "fixture"
	role := cfg.Provider.Roles["planner"]
	role.RequiredCapabilities = &config.ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}
	cfg.Provider.Roles["planner"] = role
	cfg.OpenCode = &config.OpenCodeHost{Version: 1, Executable: filepath.Join(t.TempDir(), "opencode.exe"), ExecutableHash: strings.Repeat("e", 64), StateRoot: t.TempDir()}
	if mutate != nil {
		mutate(&cfg)
	}
	repositoryRoot := t.TempDir()
	created := Creation{Version: 1, Nonce: "agent-dispatch", Repository: repository.Identity{Version: 1, Name: "fixture", Root: repositoryRoot, CommonDir: filepath.Join(repositoryRoot, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}, Objective: "Produce an implementation plan.", Config: cfg}
	if err := Append(controllerPath, "run.created", created); err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := runtime.NewInvocation(cfg.Planner, created.Objective)
	if err != nil {
		t.Fatal(err)
	}
	return controllerPath, snapshot, planner
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
