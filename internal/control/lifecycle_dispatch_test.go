package control

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskpool"
)

func TestLifecycleCrashAfterAdmissionBeforeRunningRemainsSameStage(t *testing.T) {
	controllerPath, snapshot, invocation := agentDispatchFixture(t)
	contextHash, err := access.InputID(invocation.Input)
	if err != nil {
		t.Fatal(err)
	}
	treePath := controllerPath + ".agent-tree"
	node, err := agenttree.Create(treePath, snapshot.RunID, agenttree.NodeSpec{Name: "root", Role: "planner", Authority: agenttree.AuthorityReadOnly, InvocationID: invocation.ID, ContextSHA256: contextHash})
	if err != nil {
		t.Fatal(err)
	}
	admission := agentAdmissionFor(t, snapshot, invocation, node)
	if err = appendAgentDispatchAdmission(controllerPath, admission); err != nil {
		t.Fatal(err)
	}
	if _, err = RequestPause(controllerPath, "operator", "pause-after-admission"); err != nil {
		t.Fatal(err)
	}
	paused, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := beginAgentDispatch(controllerPath, paused, invocation)
	if err != nil {
		t.Fatal("durably admitted stage could not settle after pause", err)
	}
	if binding.AdmissionID == "" || binding.Node.Status != agenttree.StatusQueued {
		t.Fatal("admission advanced before capacity was established", binding)
	}
	if err = ensureOpenCodeDispatchCapacity(paused, invocation); err != nil {
		t.Fatal(err)
	}
	if err = markAgentDispatchRunning(&binding); err != nil {
		t.Fatal("same admission did not resume from queued state", err)
	}
	if binding.Node.Status != agenttree.StatusRunning {
		t.Fatal("capacity-backed admission did not enter running", binding)
	}
	events, err := journalKinds(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if countKind(events, "agent.dispatch-admitted") != 1 {
		t.Fatal("recovery created another admission", events)
	}
}

func TestLifecyclePauseRaceOrdersBeforeOrAfterAdmission(t *testing.T) {
	for attempt := 0; attempt < 16; attempt++ {
		controllerPath, snapshot, invocation := agentDispatchFixture(t)
		admission := agentAdmissionFor(t, snapshot, invocation, agenttree.Node{AgentID: digestByte('d'), Name: "root", Path: "/root", Role: "planner", Authority: agenttree.AuthorityReadOnly, InvocationID: invocation.ID, Status: agenttree.StatusQueued})
		start := make(chan struct{})
		var wait sync.WaitGroup
		var admissionErr, pauseErr error
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			admissionErr = appendAgentDispatchAdmission(controllerPath, admission)
		}()
		go func() {
			defer wait.Done()
			<-start
			_, pauseErr = RequestPause(controllerPath, "operator", "pause-race")
		}()
		close(start)
		wait.Wait()
		if pauseErr != nil {
			t.Fatal(pauseErr)
		}
		kinds, err := journalKinds(controllerPath)
		if err != nil {
			t.Fatal(err)
		}
		pauseIndex, admissionIndex := indexKind(kinds, "run.pause-requested"), indexKind(kinds, "agent.dispatch-admitted")
		if pauseIndex < 0 {
			t.Fatal("pause request was not durable", kinds)
		}
		if admissionErr == nil && (admissionIndex < 0 || admissionIndex > pauseIndex) {
			t.Fatal("admission succeeded after pause", kinds)
		}
		if admissionErr != nil && admissionIndex >= 0 {
			t.Fatal("reported rejected admission was durable", admissionErr, kinds)
		}
	}
}

func TestLifecycleTerminalReceiptAfterCancellationIsReconciliation(t *testing.T) {
	controllerPath, snapshot, invocation := agentDispatchFixture(t)
	binding, err := beginAgentDispatch(controllerPath, snapshot, invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err = ensureOpenCodeDispatchCapacity(snapshot, invocation); err != nil {
		t.Fatal(err)
	}
	if err = markAgentDispatchRunning(&binding); err != nil {
		t.Fatal(err)
	}
	if _, err = RequestCancel(controllerPath, "operator", "cancel-running"); err != nil {
		t.Fatal(err)
	}
	if _, err = SettleLifecycle(controllerPath, "operator", "owned runtime and descendants stopped", true); err != nil {
		t.Fatal(err)
	}
	resultHash := digestByte('f')
	if err = finishAgentDispatch(binding, resultHash); err != nil {
		t.Fatal("cancelled lifecycle rejected late exact receipt", err)
	}
	settled, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := settled.AgentDispatch[invocation.ID]
	if lifecycleStatus(settled) != LifecycleCancelled || dispatch.Observation == nil || dispatch.Observation.Status != agenttree.StatusSucceeded || dispatch.Observation.ResultSHA256 != resultHash {
		t.Fatal("late receipt changed cancellation or was not retained", settled.Lifecycle, dispatch)
	}
}

func TestLifecycleUnknownDoesNotReleaseTaskPoolSlot(t *testing.T) {
	controllerPath, snapshot, invocation := agentDispatchFixture(t)
	binding, err := beginAgentDispatch(controllerPath, snapshot, invocation)
	if err != nil {
		t.Fatal(err)
	}
	configuration := snapshot.Creation.Config
	configuration.TaskPool = &config.TaskPool{Version: 1, Path: filepath.Join(t.TempDir(), "pool.db"), Limits: taskpool.Limits{Total: 1}}
	inputHash, err := access.InputID(invocation.Input)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := ResolveProviderRouting(configuration, snapshot.RunID, invocation.Profile.Role, inputHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	request, err := ensureTaskPool(configuration, snapshot.RunID, selected.Intent)
	if err != nil {
		t.Fatal(err)
	}
	if err = markAgentDispatchRunning(&binding); err != nil {
		t.Fatal(err)
	}
	if err = finishAgentDispatchUnknown(binding); err != nil {
		t.Fatal(err)
	}
	if _, err = RequestCancel(controllerPath, "operator", "cancel-unknown"); err != nil {
		t.Fatal(err)
	}
	if _, err = SettleLifecycle(controllerPath, "operator", "owned runtime and descendants stopped", true); err != nil {
		t.Fatal(err)
	}
	pool, err := taskpool.Inspect(configuration.TaskPool.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, active := pool.Active[request.ID]; !active || len(pool.Settled) != 0 {
		t.Fatal("UNKNOWN released shared capacity", pool)
	}
}

func TestLifecycleCapacityDenialParksWithoutUnknown(t *testing.T) {
	poolPath := filepath.Join(t.TempDir(), "pool.db")
	controllerPath, snapshot, invocation := agentDispatchFixtureWithConfig(t, func(configuration *config.Config) {
		configuration.TaskPool = &config.TaskPool{Version: 1, Path: poolPath, Limits: taskpool.Limits{Total: 1}}
	})
	if err := taskpool.Bind(poolPath, snapshot.Creation.Config.TaskPool.Limits); err != nil {
		t.Fatal(err)
	}
	blocker := taskpool.Request{ID: digestByte('1'), RunID: digestByte('2'), AccessProfileID: digestByte('3'), Provider: "blocked-provider", Model: "blocked-model"}
	if err := taskpool.Acquire(poolPath, blocker); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeOpenCodeProvider(context.Background(), controllerPath, snapshot, invocation, nil); !errors.Is(err, taskpool.ErrCapacity) {
		t.Fatal("expected finite pre-dispatch capacity denial", err)
	}
	state, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := state.AgentDispatch[invocation.ID]
	if dispatch.Observation != nil {
		t.Fatal("capacity denial was misclassified as attempted runtime work", dispatch)
	}
	tree, err := agenttree.Inspect(controllerPath + ".agent-tree")
	if err != nil || len(tree.Nodes) != 1 || tree.Nodes[0].Status != agenttree.StatusQueued {
		t.Fatal("capacity-denied dispatch was not parked", tree, err)
	}
}

func TestLifecycleIdentityRejectsUnboundIDsAndActions(t *testing.T) {
	for _, request := range []LifecycleRequest{
		{Version: 1, RunID: "", Action: "pause", Actor: "operator", Nonce: "n"},
		{Version: 1, RunID: digestByte('a'), Action: "stop", Actor: "operator", Nonce: "n"},
	} {
		if _, err := request.ID(); err == nil {
			t.Fatal("invalid lifecycle request acquired identity", request)
		}
	}
	if _, err := (LifecycleResume{Version: 1, RunID: "run", PauseRequestID: "pause", Actor: "operator", Nonce: "n"}).ID(); err == nil {
		t.Fatal("unbound lifecycle resume acquired identity")
	}
}

func agentAdmissionFor(t *testing.T, snapshot Snapshot, invocation runtime.Invocation, node agenttree.Node) AgentDispatchAdmission {
	t.Helper()
	contextHash, err := access.InputID(invocation.Input)
	if err != nil {
		t.Fatal(err)
	}
	node.ContextSHA256 = contextHash
	node.ResultSHA256 = ""
	node.Status = agenttree.StatusQueued
	return AgentDispatchAdmission{Version: 1, RunID: snapshot.RunID, TreeID: snapshot.RunID, Node: node, Invocation: invocation}
}

func journalKinds(path string) ([]string, error) {
	events, err := journal.Read(path)
	if err != nil {
		return nil, err
	}
	kinds := make([]string, len(events))
	for index, event := range events {
		kinds[index] = event.Kind
	}
	return kinds, nil
}

func countKind(kinds []string, want string) int {
	count := 0
	for _, kind := range kinds {
		if kind == want {
			count++
		}
	}
	return count
}

func indexKind(kinds []string, want string) int {
	for index, kind := range kinds {
		if kind == want {
			return index
		}
	}
	return -1
}

func digestByte(value byte) string { return strings.Repeat(string(value), 64) }
