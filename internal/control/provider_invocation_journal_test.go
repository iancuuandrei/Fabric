package control

import (
	"github.com/iancuuandrei/fabric/internal/contextbroker"
	"github.com/iancuuandrei/fabric/internal/opencode"
	"github.com/iancuuandrei/fabric/internal/opencoderuntime"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"strings"
	"testing"
)

func TestProviderInvocationJournalsSeparateRolesWithoutReplacingRecovery(t *testing.T) {
	path, s, inv := agentDispatchFixture(t)
	stem, err := providerInvocationJournalStem(path, inv, nil)
	if err != nil || stem != "planner" {
		t.Fatal("first turn path changed", stem, err)
	}
	binding, err := contextbroker.NewBinding(inv.ID, s.Creation.Repository, nil, contextbroker.Limits{MaxCalls: 64, MaxRequestBytes: 65536, MaxResponseBytes: 65536, MaxTotalResponseBytes: 4194304})
	if err != nil {
		t.Fatal(err)
	}
	intent := opencoderuntime.Intent{Version: 2, Invocation: inv, Directory: s.Creation.Repository.Root,
		Project: opencode.ProjectExpectation{Directory: s.Creation.Repository.Root, Mode: opencode.ProjectModeGit, Worktree: s.Creation.Repository.Root},
		Context: binding, Session: opencode.ToolSessionBinding{ToolNames: []string{}}, ProviderGatewayBindingID: strings.Repeat("e", 64),
		SessionPlan: &opencoderuntime.SessionPlan{Agent: "plan", Provider: inv.Profile.Provider, Model: inv.Profile.Model, Variant: inv.Profile.Effort, ToolNames: []string{"source_read"}, CatalogSHA256: strings.Repeat("c", 64)}}
	intent.IntentID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if err := opencoderuntime.RecordIntent(path+".planner.opencode-runtime.jsonl", intent); err != nil {
		t.Fatal(err)
	}
	stem, err = providerInvocationJournalStem(path, inv, nil)
	if err != nil || stem != "planner" {
		t.Fatal("same invocation lost its recovery path", stem, err)
	}
	next, err := runtime.NewInvocation(inv.Profile, "a different task")
	if err != nil {
		t.Fatal(err)
	}
	stem, err = providerInvocationJournalStem(path, next, nil)
	if err != nil || stem != "planner.invocation-"+next.ID {
		t.Fatal("different invocation reused previous journal", stem, err)
	}
	task := taskscheduler.TaskSpec{ControllerPath: path}
	priorPath, err := scheduledRuntimeJournal(s, inv, task, nil)
	if err != nil || priorPath != path+".planner.opencode-runtime.jsonl" {
		t.Fatal("reconciliation lost the matching historical journal", priorPath, err)
	}
	nextPath, err := scheduledRuntimeJournal(s, next, task, nil)
	if err != nil || nextPath != path+"."+stem+".opencode-runtime.jsonl" {
		t.Fatal("reconciliation selected another invocation's journal", nextPath, err)
	}
	if err := requireScheduledOfflineRuntime("opencode-http", nextPath); err == nil {
		t.Fatal("another invocation's history permitted reconciliation without current history")
	}
}
