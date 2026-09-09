package control

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskscheduler"
	"harness.local/engorch/internal/toolbridge"
)

func TestAgentToolWaitReceiptUsesSelectedControlPrefix(t *testing.T) {
	fixture := newAgentToolFixture(t)
	state := agentToolProjectionStateForTest(t, fixture)
	verifier, err := newAgentToolReceiptVerifier(fixture.controllerPath, fixture.schedulerPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	call := canonicalAgentToolCall(t, "wait_agent", 701, map[string]any{
		"agent_id": fixture.binding.Node.AgentID, "after_sequence": 0, "limit": 64, "timeout_milliseconds": 5000,
	})
	value, selectedHead, err := state.wait(context.Background(), call.Arguments)
	if err != nil || selectedHead == "" {
		t.Fatal("wait did not select an exact prefix", selectedHead, err)
	}
	if _, err := state.service.Send(context.Background(), agentcontrol.MessageRequest{
		FromAgentID: fixture.binding.Node.ParentAgentID,
		ToAgentID:   fixture.binding.Node.AgentID,
		Nonce:       "append-between-wait-and-receipt",
		Body:        "Later activity must not alter the selected wait page.",
	}); err != nil {
		t.Fatal(err)
	}
	latestHead, err := agentToolJournalHead(fixture.controllerPath + ".agent-control")
	if err != nil || latestHead == selectedHead {
		t.Fatal("later append did not advance control journal", latestHead, selectedHead, err)
	}
	result, err := state.receiptedResult(call, value, selectedHead)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeAgentToolEnvelope(t, result)
	if envelope.Sources.ControlHead != selectedHead || envelope.Sources.ActivityPageHead != selectedHead {
		t.Fatal("wait receipt did not preserve selected prefix", envelope.Sources)
	}
	assertAgentToolReceipt(t, verifier, call, result)

	// Re-signing the old page against the later prefix must fail: the later
	// activity belongs to the requested page and cannot be silently omitted.
	envelope.Sources.ControlHead = latestHead
	envelope.Sources.ActivityPageHead = latestHead
	if err := verifier.Verify(call, resignAgentToolEnvelope(t, verifier, call, envelope)); err == nil {
		t.Fatal("wait page was accepted against a different control prefix")
	}
}

func TestAgentToolWaitTimeoutMarksExactEmptyPrefixAndRetainsLegacySemantics(t *testing.T) {
	fixture := newAgentToolFixture(t)
	state := agentToolProjectionStateForTest(t, fixture)
	verifier, err := newAgentToolReceiptVerifier(fixture.controllerPath, fixture.schedulerPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	settleCtx, settleCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	_, err = state.service.WaitWithHead(settleCtx, fixture.binding.Node.AgentID, 0, 256)
	settleCancel()
	if err != nil {
		t.Fatal("initial agent status was not reconciled", err)
	}
	before, err := state.service.ActivitiesAfterWithHead(fixture.binding.Node.AgentID, 0, 256)
	if err != nil {
		t.Fatal(err)
	}
	afterSequence := 0
	if len(before.Activities) != 0 {
		afterSequence = before.Activities[len(before.Activities)-1].Sequence
	}
	call := canonicalAgentToolCall(t, "wait_agent", 702, map[string]any{
		"agent_id": fixture.binding.Node.AgentID, "after_sequence": afterSequence, "limit": 64, "timeout_milliseconds": 500,
	})
	value, selectedHead, err := state.wait(context.Background(), call.Arguments)
	if err != nil || selectedHead == "" {
		t.Fatal("timeout did not retain its last exact empty prefix", selectedHead, err)
	}
	result, err := state.receiptedResult(call, value, selectedHead)
	if err != nil {
		t.Fatal(err)
	}
	timeoutEnvelope := decodeAgentToolEnvelope(t, result)
	var timeoutOutput struct {
		Activities []agentcontrol.Activity `json:"activities"`
		TimedOut   bool                    `json:"timed_out"`
	}
	if err := canonical.Decode(timeoutEnvelope.Result, &timeoutOutput); err != nil || !timeoutOutput.TimedOut || len(timeoutOutput.Activities) != 0 {
		t.Fatal("wait did not return exact empty timeout evidence", timeoutOutput, err)
	}
	assertAgentToolReceipt(t, verifier, call, result)

	// Add an eligible activity after the timeout's selected prefix.
	if _, err := state.service.Send(context.Background(), agentcontrol.MessageRequest{
		FromAgentID: fixture.binding.Node.ParentAgentID,
		ToAgentID:   fixture.binding.Node.AgentID,
		Nonce:       "eligible-after-timeout",
		Body:        "This activity is later than the selected timeout prefix.",
	}); err != nil {
		t.Fatal(err)
	}
	latestHead, err := agentToolJournalHead(fixture.controllerPath + ".agent-control")
	if err != nil {
		t.Fatal(err)
	}

	// The new marker makes empty-timeout evidence strict at its selected head.
	strict := decodeAgentToolEnvelope(t, result)
	strict.Sources.ControlHead = latestHead
	strict.Sources.ActivityPageHead = latestHead
	if err := verifier.Verify(call, resignAgentToolEnvelope(t, verifier, call, strict)); err == nil {
		t.Fatal("marked timeout admitted an eligible activity at its prefix")
	}

	// Historical timeout receipts omitted the marker and treated timeout as a
	// timing observation. Keep those bytes/verifier semantics recoverable.
	legacy := strict
	legacy.Sources.ActivityPageHead = ""
	if err := verifier.Verify(call, resignAgentToolEnvelope(t, verifier, call, legacy)); err != nil {
		t.Fatal("legacy timing-only timeout receipt became unrecoverable", err)
	}

	// A marker is valid only when it exactly names the recorded control prefix.
	mismatch := decodeAgentToolEnvelope(t, result)
	mismatch.Sources.ActivityPageHead = latestHead
	if err := verifier.Verify(call, resignAgentToolEnvelope(t, verifier, call, mismatch)); err == nil {
		t.Fatal("mismatched wait activity prefix admitted")
	}
}

func TestAgentToolActivityPageMarkerIsWaitOnly(t *testing.T) {
	fixture := newAgentToolFixture(t)
	verifier, err := newAgentToolReceiptVerifier(fixture.controllerPath, fixture.schedulerPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	call := canonicalAgentToolCall(t, "list_agents", 703, map[string]any{"limit": 4})
	result := executeAgentToolCall(t, fixture.projection, call)
	envelope := decodeAgentToolEnvelope(t, result)
	envelope.Sources.ActivityPageHead = envelope.Sources.ControlHead
	if err := verifier.Verify(call, resignAgentToolEnvelope(t, verifier, call, envelope)); err == nil {
		t.Fatal("non-wait receipt admitted an activity page marker")
	}
}

func TestAgentToolWaitDeliveryProjectionRejectsResignedPagination(t *testing.T) {
	fixture := newAgentToolFixture(t)
	verifier, err := newAgentToolReceiptVerifier(fixture.controllerPath, fixture.schedulerPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	call := canonicalAgentToolCall(t, "wait_agent", 705, map[string]any{
		"agent_id": fixture.binding.Node.AgentID, "after_sequence": 0, "limit": 64, "timeout_milliseconds": 5000,
	})
	result := executeAgentToolCall(t, fixture.projection, call)
	envelope := decodeAgentToolEnvelope(t, result)
	var output waitAgentOutput
	if err := canonical.Decode(envelope.Result, &output); err != nil || output.DeliveryVersion != waitAgentDeliveryVersion || len(output.Activities) == 0 || output.Truncated || output.NextAfter != 0 {
		t.Fatal("versioned wait delivery differs", output, err)
	}
	assertAgentToolReceipt(t, verifier, call, result)
	output.Truncated = true
	output.NextAfter = output.Activities[len(output.Activities)-1].Sequence
	envelope.Result, err = canonical.Bytes(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(call, resignAgentToolEnvelope(t, verifier, call, envelope)); err == nil {
		t.Fatal("re-signed arbitrary wait truncation admitted")
	}
}

func TestAgentToolWaitPreexpiredContextReturnsNoPrefix(t *testing.T) {
	fixture := newAgentToolFixture(t)
	state := agentToolProjectionStateForTest(t, fixture)
	call := canonicalAgentToolCall(t, "wait_agent", 704, map[string]any{
		"agent_id": fixture.binding.Node.AgentID, "after_sequence": 1000, "limit": 1, "timeout_milliseconds": 500,
	})
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	value, head, err := state.wait(expired, call.Arguments)
	if err == nil || value != nil || head != "" {
		t.Fatal("pre-expired wait fabricated result prefix", value, head, err)
	}
}

func TestAgentToolWaitDeliversAcceptedExplorerResultToParentAndChildCursor(t *testing.T) {
	fixture := newAcceptedAgentToolFixture(t)
	child, accepted, exploration := indexAgentToolAcceptedChild(t, fixture, "one", "Delivered accepted child result.", []string{"file.txt"})
	verifier, err := newAgentToolReceiptVerifier(fixture.controllerPath, fixture.schedulerPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}

	parentCall := canonicalAgentToolCall(t, "wait_agent", 706, map[string]any{
		"agent_id": fixture.binding.Node.AgentID, "after_sequence": 0, "limit": 64, "timeout_milliseconds": 5000,
	})
	parentCall.RequestID = json.RawMessage(`"` + strings.Repeat("<", 254) + `"`)
	parentResult := executeAgentToolCall(t, fixture.projection, parentCall)
	wire, err := toolbridge.EncodeToolResult(parentCall.RequestID, parentResult)
	if err != nil || len(wire) > contextmcp.MaxWireResponseBytes {
		t.Fatal("accepted wait result exceeded exact MCP wire bound", len(wire), err)
	}
	parentEnvelope := decodeAgentToolEnvelope(t, parentResult)
	var parentOutput waitAgentOutput
	if err := canonical.Decode(parentEnvelope.Result, &parentOutput); err != nil {
		t.Fatal(err)
	}
	assertAcceptedWaitBody(t, parentOutput, accepted, exploration)
	assertAgentToolReceipt(t, verifier, parentCall, parentResult)

	controlState, err := agentcontrol.Inspect(fixture.controllerPath + ".agent-control")
	if err != nil {
		t.Fatal(err)
	}
	childResultSequence := 0
	for _, activity := range controlState.Activities {
		if activity.AgentID == child.AgentID && activity.Kind == "result" && activity.ResultID == accepted.ResultID {
			childResultSequence = activity.Sequence
		}
	}
	if childResultSequence < 2 {
		t.Fatal("child result notification did not follow terminal activity", childResultSequence)
	}
	childCall := canonicalAgentToolCall(t, "wait_agent", 707, map[string]any{
		"agent_id": child.AgentID, "after_sequence": childResultSequence - 1, "limit": 64, "timeout_milliseconds": 5000,
	})
	childResult := executeAgentToolCall(t, fixture.projection, childCall)
	childEnvelope := decodeAgentToolEnvelope(t, childResult)
	var childOutput waitAgentOutput
	if err := canonical.Decode(childEnvelope.Result, &childOutput); err != nil {
		t.Fatal(err)
	}
	assertAcceptedWaitBody(t, childOutput, accepted, exploration)
	assertAgentToolReceipt(t, verifier, childCall, childResult)

	// Removing the delivery fields cannot downgrade a result activity into the
	// historical activity-only receipt contract.
	downgraded := parentOutput
	downgraded.DeliveryVersion = 0
	downgraded.AcceptedResults = nil
	parentEnvelope.Result, err = canonical.Bytes(downgraded)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(parentCall, resignAgentToolEnvelope(t, verifier, parentCall, parentEnvelope)); err == nil {
		t.Fatal("accepted result page downgraded to legacy activity-only receipt")
	}

	// Even a correctly re-signed envelope cannot substitute the accepted body.
	tampered := decodeAgentToolEnvelope(t, parentResult)
	var tamperedOutput waitAgentOutput
	if err := canonical.Decode(tampered.Result, &tamperedOutput); err != nil {
		t.Fatal(err)
	}
	tamperedOutput.AcceptedResults[0].Exploration.Summary = "Substituted body."
	tampered.Result, err = canonical.Bytes(tamperedOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(parentCall, resignAgentToolEnvelope(t, verifier, parentCall, tampered)); err == nil {
		t.Fatal("re-signed accepted result body substitution admitted")
	}
	tampered = decodeAgentToolEnvelope(t, parentResult)
	if err := canonical.Decode(tampered.Result, &tamperedOutput); err != nil {
		t.Fatal(err)
	}
	tamperedOutput.AcceptedResults[0].Result.Reference.ControllerEventHash = strings.Repeat("9", 64)
	tampered.Result, err = canonical.Bytes(tamperedOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(parentCall, resignAgentToolEnvelope(t, verifier, parentCall, tampered)); err == nil {
		t.Fatal("re-signed accepted controller event substitution admitted")
	}

	// Later valid controller, tree, control and scheduler appends do not alter
	// either historical accepted-result receipt.
	_, _, _ = indexAgentToolAcceptedChild(t, fixture, "later", "Later accepted result.", []string{"later.txt"})
	assertAgentToolReceipt(t, verifier, parentCall, parentResult)
	assertAgentToolReceipt(t, verifier, childCall, childResult)
}

func TestAgentToolWaitPaginatesLargestExactWirePrefixWithoutLoss(t *testing.T) {
	fixture := newAcceptedAgentToolFixture(t)
	paths := make([]string, 64)
	paths[0] = strings.Repeat("&", 1024)
	for index := 1; index < len(paths); index++ {
		value := []byte(strings.Repeat("&", 1024))
		value[index] = '/'
		paths[index] = string(value)
	}
	sort.Strings(paths)
	summary := strings.Repeat("\x00", 8192)
	_, first, _ := indexAgentToolAcceptedChild(t, fixture, "large-one", summary, paths)
	_, second, _ := indexAgentToolAcceptedChild(t, fixture, "large-two", summary, paths)
	verifier, err := newAgentToolReceiptVerifier(fixture.controllerPath, fixture.schedulerPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	requestID := json.RawMessage(`"` + strings.Repeat("<", 254) + `"`)
	firstCall := canonicalAgentToolCall(t, "wait_agent", 708, map[string]any{
		"agent_id": fixture.binding.Node.AgentID, "after_sequence": 0, "limit": 64, "timeout_milliseconds": 5000,
	})
	firstCall.RequestID = requestID
	firstResult := executeAgentToolCall(t, fixture.projection, firstCall)
	firstEnvelope := decodeAgentToolEnvelope(t, firstResult)
	var firstPage waitAgentOutput
	if err := canonical.Decode(firstEnvelope.Result, &firstPage); err != nil || !firstPage.Truncated || firstPage.NextAfter < 1 || len(firstPage.AcceptedResults) != 1 || firstPage.AcceptedResults[0].Result != first {
		t.Fatal("first exact-wire page differs", len(firstPage.Activities), len(firstPage.AcceptedResults), firstPage.Truncated, firstPage.NextAfter, err)
	}
	firstWire, err := toolbridge.EncodeToolResult(firstCall.RequestID, firstResult)
	if err != nil || len(firstWire) > contextmcp.MaxWireResponseBytes {
		t.Fatal("first exact-wire page exceeded bound", len(firstWire), err)
	}
	assertAgentToolReceipt(t, verifier, firstCall, firstResult)

	secondCall := canonicalAgentToolCall(t, "wait_agent", 709, map[string]any{
		"agent_id": fixture.binding.Node.AgentID, "after_sequence": firstPage.NextAfter, "limit": 64, "timeout_milliseconds": 5000,
	})
	secondCall.RequestID = json.RawMessage(`"` + strings.Repeat("<", 253) + `2"`)
	secondResult := executeAgentToolCall(t, fixture.projection, secondCall)
	secondEnvelope := decodeAgentToolEnvelope(t, secondResult)
	var secondPage waitAgentOutput
	if err := canonical.Decode(secondEnvelope.Result, &secondPage); err != nil || secondPage.Truncated || len(secondPage.AcceptedResults) != 1 || secondPage.AcceptedResults[0].Result != second {
		t.Fatal("second exact-wire page lost accepted result", len(secondPage.Activities), len(secondPage.AcceptedResults), secondPage.Truncated, err)
	}
	secondWire, err := toolbridge.EncodeToolResult(secondCall.RequestID, secondResult)
	if err != nil || len(secondWire) > contextmcp.MaxWireResponseBytes {
		t.Fatal("second exact-wire page exceeded bound", len(secondWire), err)
	}
	assertAgentToolReceipt(t, verifier, secondCall, secondResult)
	t.Logf("accepted wait wire bytes: first=%d second=%d limit=%d", len(firstWire), len(secondWire), contextmcp.MaxWireResponseBytes)
}

func TestAgentToolWaitOmitsGrandchildBodyWithoutSkippingActivity(t *testing.T) {
	fixture := newAcceptedAgentToolFixture(t)
	direct, directResult, _ := indexAgentToolAcceptedChild(t, fixture, "direct", "Direct result.", []string{"direct.txt"})
	state, err := agentcontrol.Inspect(fixture.controllerPath + ".agent-control")
	if err != nil {
		t.Fatal(err)
	}
	afterDirect := 0
	for _, activity := range state.Activities {
		if activity.AgentID == direct.AgentID && activity.Kind == "result" && activity.ResultID == directResult.ResultID {
			afterDirect = activity.Sequence
		}
	}
	if afterDirect == 0 {
		t.Fatal("direct child result cursor unavailable")
	}
	_, grandchildResult, grandchildExploration := indexAgentToolAcceptedUnder(t, fixture, direct.AgentID, "grandchild", "Grandchild result outside caller scope.", []string{"grandchild.txt"})
	verifier, err := newAgentToolReceiptVerifier(fixture.controllerPath, fixture.schedulerPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	call := canonicalAgentToolCall(t, "wait_agent", 710, map[string]any{
		"agent_id": direct.AgentID, "after_sequence": afterDirect, "limit": 64, "timeout_milliseconds": 5000,
	})
	result := executeAgentToolCall(t, fixture.projection, call)
	envelope := decodeAgentToolEnvelope(t, result)
	var output waitAgentOutput
	if err := canonical.Decode(envelope.Result, &output); err != nil {
		t.Fatal(err)
	}
	resultSequence, resultActivities := 0, 0
	for _, activity := range output.Activities {
		if activity.Kind == "result" && activity.ResultID == grandchildResult.ResultID {
			resultSequence, resultActivities = activity.Sequence, resultActivities+1
		}
	}
	if resultActivities != 1 || len(output.AcceptedResults) != 0 || len(output.ResultOmissions) != 1 || output.ResultOmissions[0].ActivitySequence != resultSequence || output.ResultOmissions[0].ResultID != grandchildResult.ResultID || output.ResultOmissions[0].Reason != "outside-caller-result-scope" {
		t.Fatal("grandchild result was skipped or exposed", output, err)
	}
	assertAgentToolReceipt(t, verifier, call, result)

	// A re-signed caller cannot turn the explicit omission into a body grant.
	output.AcceptedResults = []waitAgentAcceptedResult{{ActivitySequence: resultSequence, Result: grandchildResult, Exploration: grandchildExploration}}
	output.ResultOmissions = nil
	envelope.Result, err = canonical.Bytes(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(call, resignAgentToolEnvelope(t, verifier, call, envelope)); err == nil {
		t.Fatal("grandchild omission was re-signed into an accepted body")
	}
}

func TestAgentToolWaitBindsIdenticalFollowUpBodyToDistinctTurn(t *testing.T) {
	fixture := newAcceptedAgentToolFixture(t)
	summary := "Identical accepted summary."
	child, first, _ := indexAgentToolAcceptedChild(t, fixture, "initial-identical", summary, []string{"same.txt"})
	controlState, err := agentcontrol.Inspect(fixture.controllerPath + ".agent-control")
	if err != nil {
		t.Fatal(err)
	}
	afterFirst := 0
	for _, activity := range controlState.Activities {
		if activity.AgentID == fixture.binding.Node.AgentID && activity.Kind == "result" && activity.ResultID == first.ResultID {
			afterFirst = activity.Sequence
		}
	}
	question := "Inspect the distinct follow-up turn."
	_, dynamic, err := FollowUpExplorerAgent(context.Background(), fixture.controllerPath, fixture.schedulerPath, agentcontrol.MessageRequest{
		FromAgentID: fixture.binding.Node.AgentID, ToAgentID: child.AgentID, Nonce: "identical-follow-up", Body: question,
	}, question)
	if err != nil {
		t.Fatal(err)
	}
	second, secondExploration := acceptAgentToolDynamic(t, fixture, dynamic, summary, []string{"same.txt"})
	if first.ResultID == second.ResultID || first.Reference.TurnID == second.Reference.TurnID || first.Reference.InvocationID == second.Reference.InvocationID || first.Reference.TurnSequence+1 != second.Reference.TurnSequence {
		t.Fatal("follow-up accepted result identity collapsed", first, second)
	}
	verifier, err := newAgentToolReceiptVerifier(fixture.controllerPath, fixture.schedulerPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	call := canonicalAgentToolCall(t, "wait_agent", 711, map[string]any{
		"agent_id": fixture.binding.Node.AgentID, "after_sequence": afterFirst, "limit": 64, "timeout_milliseconds": 5000,
	})
	result := executeAgentToolCall(t, fixture.projection, call)
	envelope := decodeAgentToolEnvelope(t, result)
	var output waitAgentOutput
	if err := canonical.Decode(envelope.Result, &output); err != nil {
		t.Fatal(err)
	}
	assertAcceptedWaitBody(t, output, second, secondExploration)
	assertAgentToolReceipt(t, verifier, call, result)
}

func TestAgentToolWaitRejectsHistoricalAndIdentitySubstitution(t *testing.T) {
	fixture := newAcceptedAgentToolFixture(t)
	_, accepted, _ := indexAgentToolAcceptedChild(t, fixture, "substitution", "Exact accepted result.", []string{"exact.txt"})
	verifier, err := newAgentToolReceiptVerifier(fixture.controllerPath, fixture.schedulerPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	call := canonicalAgentToolCall(t, "wait_agent", 712, map[string]any{
		"agent_id": fixture.binding.Node.AgentID, "after_sequence": 0, "limit": 64, "timeout_milliseconds": 5000,
	})
	result := executeAgentToolCall(t, fixture.projection, call)
	base := decodeAgentToolEnvelope(t, result)
	var output waitAgentOutput
	if err := canonical.Decode(base.Result, &output); err != nil || len(output.AcceptedResults) != 1 {
		t.Fatal("accepted substitution fixture differs", output, err)
	}

	events, err := journal.Read(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	priorHead := ""
	for index, event := range events {
		if event.Hash == accepted.Reference.ControllerEventHash && index > 0 {
			priorHead = events[index-1].Hash
		}
	}
	if priorHead == "" {
		t.Fatal("historical controller prefix before accepted event unavailable")
	}
	historical := base
	historical.Sources.ControllerHead = priorHead
	if err := verifier.Verify(call, resignAgentToolEnvelope(t, verifier, call, historical)); err == nil {
		t.Fatal("accepted result resolved from a controller prefix before its event")
	}

	for name, mutate := range map[string]func(*waitAgentOutput){
		"turn": func(changed *waitAgentOutput) {
			changed.AcceptedResults[0].Result.Reference.TurnID = strings.Repeat("7", 64)
		},
		"admission": func(changed *waitAgentOutput) {
			changed.AcceptedResults[0].Result.Reference.AdmissionID = strings.Repeat("7", 64)
		},
		"activity-sequence": func(changed *waitAgentOutput) {
			changed.AcceptedResults[0].ActivitySequence++
		},
	} {
		t.Run(name, func(t *testing.T) {
			changedEnvelope := base
			var changed waitAgentOutput
			if err := canonical.Decode(base.Result, &changed); err != nil {
				t.Fatal(err)
			}
			mutate(&changed)
			changedEnvelope.Result, err = canonical.Bytes(changed)
			if err != nil {
				t.Fatal(err)
			}
			if err := verifier.Verify(call, resignAgentToolEnvelope(t, verifier, call, changedEnvelope)); err == nil {
				t.Fatal("re-signed accepted result substitution admitted")
			}
		})
	}
}

func indexAgentToolAcceptedChild(t *testing.T, fixture agentToolFixture, suffix, summary string, paths []string) (agenttree.Node, agentcontrol.AcceptedResult, Exploration) {
	t.Helper()
	return indexAgentToolAcceptedUnder(t, fixture, fixture.binding.Node.AgentID, suffix, summary, paths)
}

func indexAgentToolAcceptedUnder(t *testing.T, fixture agentToolFixture, parentID, suffix, summary string, paths []string) (agenttree.Node, agentcontrol.AcceptedResult, Exploration) {
	t.Helper()
	question := "Inspect the exact accepted child result " + suffix + "."
	child, dynamic, err := SpawnExplorerAgent(context.Background(), fixture.controllerPath, fixture.schedulerPath, parentID, "accepted-child-"+suffix, question, "accepted-child-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	accepted, exploration := acceptAgentToolDynamic(t, fixture, dynamic, summary, paths)
	return child, accepted, exploration
}

func acceptAgentToolDynamic(t *testing.T, fixture agentToolFixture, dynamic taskscheduler.DynamicTask, summary string, paths []string) (agentcontrol.AcceptedResult, Exploration) {
	t.Helper()
	turn := taskscheduler.AgentTurnBinding{ParentAgentID: dynamic.ParentAgentID, AgentID: dynamic.AgentID, TurnID: dynamic.TurnID, TurnSequence: dynamic.TurnSequence}
	snapshot, _, invocation, err := scheduledInvocation(dynamic.Task, &turn)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := beginAgentDispatchForTurn(fixture.controllerPath, snapshot, invocation, &turn)
	if err != nil {
		t.Fatal(err)
	}
	if err := markAgentDispatchRunning(&binding); err != nil {
		t.Fatal(err)
	}
	candidateID, err := snapshot.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	exploration := Exploration{CandidateID: candidateID, Summary: summary, Paths: append([]string(nil), paths...)}
	output, err := compactExplorationJSON(exploration)
	if err != nil {
		t.Fatal(err)
	}
	model := invocation.Profile.Model
	record := ExplorerRecord{Question: dynamic.Task.Input, Invocation: invocation, Result: runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, Output: string(output)}}
	resultHash, err := canonical.Hash("harness.explorer-result.v1", record.Result)
	if err != nil {
		t.Fatal(err)
	}
	if err := finishAgentDispatch(binding, resultHash); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordExploration(fixture.controllerPath, record); err != nil {
		t.Fatal(err)
	}
	claim := taskscheduler.Claim{Version: 1, ScheduleID: strings.Repeat("7", 64), Task: dynamic.Task, Generation: 1, ControllerHead: strings.Repeat("8", 64), AgentTurn: &turn}
	indexed, err := ensureAcceptedExplorerResult(context.Background(), fixture.controllerPath, claim, invocation, &record)
	if err != nil || !indexed {
		t.Fatal("accepted child result was not indexed", indexed, err)
	}
	state, err := agentcontrol.Inspect(fixture.controllerPath + ".agent-control")
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range state.Results {
		if result.Reference.TurnID == turn.TurnID {
			return result, exploration
		}
	}
	t.Fatal("accepted child result unavailable")
	return agentcontrol.AcceptedResult{}, Exploration{}
}

func compactExplorationJSON(exploration Exploration) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(exploration); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte("\n")), nil
}

func newAcceptedAgentToolFixture(t *testing.T) agentToolFixture {
	t.Helper()
	base := newAcceptedExplorerFixture(t)
	seed, err := PrepareScheduledTask(base.controllerPath, taskscheduler.OperationPlanner, "")
	if err != nil {
		t.Fatal(err)
	}
	seed.ID = strings.Repeat("a", 64)
	schedulerPath := filepath.Join(t.TempDir(), "accepted-agent-tools.jsonl")
	if _, err := taskscheduler.Bind(schedulerPath, taskscheduler.Definition{Version: 1, Nonce: "accepted-agent-tools", Tasks: []taskscheduler.TaskSpec{seed}}); err != nil {
		t.Fatal(err)
	}
	_, dynamic, err := SpawnExplorerAgent(context.Background(), base.controllerPath, schedulerPath, base.root.AgentID, "accepted-caller", "Coordinate accepted child evidence.", "accepted-caller-once")
	if err != nil {
		t.Fatal(err)
	}
	adapter := interruptSchedulerAdapter{seedID: seed.ID}
	if decision, err := taskscheduler.Tick(context.Background(), schedulerPath, adapter); err != nil || decision.TaskID != seed.ID || decision.Status != taskscheduler.StatusSucceeded {
		t.Fatal("accepted tool seed scheduling failed", decision, err)
	}
	if decision, err := taskscheduler.Tick(context.Background(), schedulerPath, adapter); err != nil || decision.TaskID != dynamic.TurnID || decision.Status != taskscheduler.StatusRunning {
		t.Fatal("accepted tool caller scheduling failed", decision, err)
	}
	scheduled, err := taskscheduler.Inspect(schedulerPath)
	if err != nil || scheduled.Tasks[dynamic.TurnID].Claim == nil {
		t.Fatal("accepted tool caller claim unavailable", err)
	}
	claim := *scheduled.Tasks[dynamic.TurnID].Claim
	snapshot, _, invocation, err := scheduledInvocation(claim.Task, claim.AgentTurn)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := beginAgentDispatchForTurn(base.controllerPath, snapshot, invocation, claim.AgentTurn)
	if err != nil {
		t.Fatal(err)
	}
	if err := markAgentDispatchRunning(&binding); err != nil {
		t.Fatal(err)
	}
	projection, err := newAgentToolProjection(base.controllerPath, schedulerPath, binding)
	if err != nil {
		t.Fatal(err)
	}
	return agentToolFixture{
		interruptSchedulerAdapter: adapter,
		controllerPath:            base.controllerPath,
		schedulerPath:             schedulerPath,
		binding:                   binding,
		projection:                projection,
	}
}

func assertAcceptedWaitBody(t *testing.T, output waitAgentOutput, accepted agentcontrol.AcceptedResult, exploration Exploration) {
	t.Helper()
	if output.DeliveryVersion != waitAgentDeliveryVersion || len(output.AcceptedResults) != 1 || output.AcceptedResults[0].Result != accepted || !sameCanonical(output.AcceptedResults[0].Exploration, exploration) || len(output.ResultOmissions) != 0 {
		raw, _ := json.Marshal(output)
		t.Fatal("accepted wait result differs", string(raw))
	}
	activitySequence := output.AcceptedResults[0].ActivitySequence
	found := false
	for _, activity := range output.Activities {
		found = found || activity.Sequence == activitySequence && activity.Kind == "result" && activity.ResultID == accepted.ResultID
	}
	if !found {
		t.Fatal("accepted body lacks exact activity association", output)
	}
}

func agentToolProjectionStateForTest(t *testing.T, fixture agentToolFixture) *agentToolProjectionState {
	t.Helper()
	state := &agentToolProjectionState{
		controllerPath: fixture.controllerPath,
		schedulerPath:  fixture.schedulerPath,
		binding:        fixture.binding,
	}
	service, claimID, controllerHead, scheduleID, err := state.validate()
	if err != nil {
		t.Fatal(err)
	}
	state.service = service
	state.claimID = claimID
	state.controllerHead = controllerHead
	state.scheduleID = scheduleID
	return state
}
