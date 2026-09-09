package opencoderuntime

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/toolbridge"
	"harness.local/engorch/internal/toolreceipts"
)

func TestCompositeBoundCapturesUnusedReceiptPrefix(t *testing.T) {
	fixture := newRuntimeFixture(t)
	intent := compositeIntent(t, fixture.intent)
	runtimePath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := RecordIntent(runtimePath, intent); err != nil {
		t.Fatal(err)
	}
	sessionBinding, err := intent.ResolveToolSessionBinding("global")
	if err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	appendUnchecked(t, sessionPath, "opencode.tool-session-intent", sessionBinding)
	appendUnchecked(t, sessionPath, "opencode.tool-session-observed", struct {
		Binding opencode.ToolSessionBinding `json:"binding"`
		ID      string                      `json:"id"`
	}{sessionBinding, "ses_fixture"})
	receiptPath := filepath.Join(t.TempDir(), "receipts.jsonl")
	appendUnchecked(t, receiptPath, "mcp_tool_receipts_bound_v1", *intent.ToolReceipts)
	paths := fixture.paths
	paths.Version, paths.Session, paths.Dispatch, paths.Seal, paths.ToolReceipts = 2, sessionPath, filepath.Join(t.TempDir(), "dispatch.jsonl"), filepath.Join(t.TempDir(), "seal.jsonl"), receiptPath
	expected := fixture.bound.SealExpected
	expected.Session = sessionBinding
	expected.Tools.ToolIDs = []string{"engorch_agent_send", "engorch_source_list"}
	bound, err := RecordBound(runtimePath, intent, fixture.bound.Project, paths, expected)
	if err != nil {
		t.Fatal("record composite bound", err)
	}
	if bound.Version != 2 || bound.Composite == nil || bound.Composite.Dispatch.ReceiptPath != receiptPath || bound.Composite.Dispatch.Receipts.BindingID != intent.ToolReceipts.BindingID || bound.Composite.ReceiptInitialHead == "" || bound.Composite.ReceiptInitialStateID == "" {
		t.Fatal("composite bound lacks exact unused receipt prefix", bound)
	}
	state, err := InspectComposite(runtimePath, intent, func(toolreceipts.Owner, toolbridge.Call, toolbridge.Result) error { return nil })
	if err != nil || state.Bound == nil || !equalCanonical(*state.Bound, bound) {
		t.Fatal("inspect composite bound", state, err)
	}
	if _, err := Inspect(runtimePath, intent); !errors.Is(err, errCompositeAPIRequired) {
		t.Fatal("legacy inspector admitted composite bound", err)
	}
}

func TestCompositeIntentRecordsAndReturnsDetachedReceiptBinding(t *testing.T) {
	fixture := newRuntimeFixture(t)
	intent := compositeIntent(t, fixture.intent)
	expected := normalizedIntent(intent)
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := RecordIntent(path, intent); err != nil {
		t.Fatal("record composite intent", err)
	}

	intent.ToolReceipts.Tools[0].Tool = "mutated"
	intent.SessionPlan.ToolNames[0] = "mutated"
	state, err := Inspect(path, expected)
	if err != nil || state.Intent == nil || state.Bound != nil || state.Intent.ToolReceipts == nil {
		t.Fatal("inspect detached composite intent", state, err)
	}
	if state.Intent.ToolReceipts.Tools[0].Tool != "source_list" || state.Intent.SessionPlan.ToolNames[0] != "agent_send" {
		t.Fatal("caller mutation changed durable composite intent", state.Intent)
	}
	state.Intent.ToolReceipts.Tools[0].Tool = "again"
	again, err := Inspect(path, expected)
	if err != nil || again.Intent.ToolReceipts.Tools[0].Tool != "source_list" {
		t.Fatal("returned binding aliases replay state", again, err)
	}
}

func TestCompositeIntentRejectsIncompleteOrMismatchedReceiptBinding(t *testing.T) {
	fixture := newRuntimeFixture(t)
	valid := compositeIntent(t, fixture.intent)
	tests := map[string]func(*Intent){
		"missing": func(intent *Intent) { intent.ToolReceipts = nil },
		"invocation": func(intent *Intent) {
			intent.ToolReceipts.InvocationID = strings.Repeat("9", 64)
			resetReceiptBindingID(intent.ToolReceipts)
		},
		"catalog": func(intent *Intent) {
			intent.ToolReceipts.CatalogSHA256 = strings.Repeat("8", 64)
			resetReceiptBindingID(intent.ToolReceipts)
		},
		"binding_id": func(intent *Intent) { intent.ToolReceipts.BindingID = strings.Repeat("7", 64) },
		"owner_map": func(intent *Intent) {
			intent.ToolReceipts.Tools[1].Owner = toolreceipts.OwnerContext
			intent.ToolReceipts.BindingID = ""
		},
		"tool_names": func(intent *Intent) {
			intent.SessionPlan.ToolNames[0] = "candidate_read"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			intent := normalizedIntent(valid)
			intent.IntentID = ""
			mutate(&intent)
			if _, err := intent.ID(); err == nil {
				t.Fatal("invalid composite receipt binding admitted")
			}
		})
	}

	legacy := valid
	legacy.Version = 2
	legacy.IntentID = ""
	if _, err := legacy.ID(); err == nil {
		t.Fatal("v2 intent admitted composite receipt binding")
	}
}

func TestCompositePathsAreVersionedUniqueAndCannotUseLegacySeal(t *testing.T) {
	root := t.TempDir()
	legacy := Paths{Version: 1, Session: filepath.Join(root, "session"), Dispatch: filepath.Join(root, "dispatch"), Broker: filepath.Join(root, "broker"), Seal: filepath.Join(root, "seal")}
	composite := legacy
	composite.Version = 2
	composite.ToolReceipts = filepath.Join(root, "receipts")
	if err := composite.validate(); err != nil {
		t.Fatal("valid composite paths rejected", err)
	}
	duplicate := composite
	duplicate.ToolReceipts = duplicate.Broker
	if err := duplicate.validate(); err == nil {
		t.Fatal("duplicate composite receipt path admitted")
	}
	missing := composite
	missing.ToolReceipts = ""
	if err := missing.validate(); err == nil {
		t.Fatal("composite paths admitted without receipt journal")
	}
	legacy.ToolReceipts = filepath.Join(root, "unexpected")
	if err := legacy.validate(); err == nil {
		t.Fatal("legacy paths admitted a receipt journal")
	}

	fixture := newRuntimeFixture(t)
	intent := compositeIntent(t, fixture.intent)
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := RecordIntent(path, intent); err != nil {
		t.Fatal("record composite intent", err)
	}
	if _, err := Complete(path, intent); !errors.Is(err, errCompositeAPIRequired) {
		t.Fatal("composite intent reached legacy completion", err)
	}
}

func TestIntentAndPathVersionsMustMatch(t *testing.T) {
	fixture := newRuntimeFixture(t)
	composite := compositeIntent(t, fixture.intent)
	legacyPaths := fixture.paths
	if err := validateIntentPaths(composite, legacyPaths); err == nil {
		t.Fatal("composite intent admitted legacy paths")
	}
	compositePaths := legacyPaths
	compositePaths.Version = 2
	compositePaths.ToolReceipts = filepath.Join(t.TempDir(), "receipts")
	if err := validateIntentPaths(fixture.intent, compositePaths); err == nil {
		t.Fatal("legacy intent admitted composite paths")
	}
}

func compositeIntent(t *testing.T, legacy Intent) Intent {
	t.Helper()
	plan := &SessionPlan{
		Agent: legacy.Session.Session.Agent, Provider: legacy.Session.Session.Provider,
		Model: legacy.Session.Session.Model, Variant: legacy.Session.Session.Variant,
		ToolNames: append([]string{"agent_send"}, legacy.Session.ToolNames...), CatalogSHA256: legacy.Session.CatalogSHA256,
	}
	binding := toolreceipts.Binding{
		Version:             1,
		InvocationID:        legacy.Invocation.ID,
		CallerBindingSHA256: strings.Repeat("b", 64),
		CatalogSHA256:       plan.CatalogSHA256,
		Tools: []toolreceipts.ToolOwner{
			{Tool: "source_list", Owner: toolreceipts.OwnerContext},
			{Tool: "agent_send", Owner: toolreceipts.OwnerAgent},
		},
	}
	resetReceiptBindingID(&binding)
	intent := legacy
	intent.Version = 3
	intent.IntentID = ""
	intent.Session = opencode.ToolSessionBinding{}
	intent.SessionPlan = plan
	intent.ToolReceipts = &binding
	if _, err := intent.ID(); err != nil {
		t.Fatal("construct composite intent", err)
	}
	return intent
}

func resetReceiptBindingID(binding *toolreceipts.Binding) {
	binding.BindingID = ""
	binding.BindingID, _ = binding.ID()
}
