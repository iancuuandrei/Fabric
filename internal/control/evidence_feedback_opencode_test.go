package control

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexhost"
	"github.com/iancuuandrei/fabric/internal/evidencevalue"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func openCodeFeedbackEntry(id string) OpenCodeUsageEntry {
	cached, reasoning := int64(16), int64(4)
	uncached, ordinary := int64(32), int64(8)
	calls, receipts, pending := 2, 2, 0
	return OpenCodeUsageEntry{
		InvocationID: id, Role: "writer", JournalPresent: true, ReceiptMatched: true, Status: "completed",
		Usage:           &OpenCodeTokenUsage{InputTokens: 48, CachedInputTokens: &cached, UncachedInputTokens: &uncached, OutputTokens: 12, ReasoningTokens: &reasoning, OrdinaryOutputTokens: &ordinary},
		GatewayCalls:    &calls,
		GatewayReceipts: &receipts,
		GatewayPending:  &pending,
	}
}

func openCodeFeedbackUsage(ids ...string) RunUsage {
	usage := RunUsage{}
	for _, id := range ids {
		usage.OpenCodeInvocations = append(usage.OpenCodeInvocations, openCodeFeedbackEntry(id))
	}
	usage.controllerOrder = append([]string{}, ids...)
	return usage
}

func feedbackCostsText(costs map[string]*evidencevalue.Numeric) map[string]string {
	values := map[string]string{}
	for name, cost := range costs {
		if cost != nil {
			values[name] = string(*cost)
		}
	}
	return values
}

func TestOpenCodeFeedbackCompletePartitionsAndKnownZeroVsNull(t *testing.T) {
	usage := openCodeFeedbackUsage(strings.Repeat("b", 64))
	observations, unavailable, err := evidenceTokenObservations(usage)
	if err != nil || len(unavailable) != 0 || len(observations) != 1 {
		t.Fatal("complete OpenCode entry must contribute", err, unavailable)
	}
	want := map[string]string{"uncached_input_tokens": "32", "cached_input_tokens": "16", "ordinary_output_tokens": "8", "reasoning_output_tokens": "4"}
	if got := feedbackCostsText(observations[0].Costs); !reflect.DeepEqual(got, want) {
		t.Fatal("complete partitions mispriced", got)
	}
	if observations[0].Costs["money_minor_units"] != nil || observations[0].Costs["wall_ms"] != nil || observations[0].Costs["input_tokens"] != nil || observations[0].Costs["cache_write_tokens"] != nil {
		t.Fatal("non-axis totals invented")
	}

	zero := openCodeFeedbackEntry(strings.Repeat("c", 64))
	cachedZero, reasoningZero := int64(0), int64(0)
	uncachedFull, ordinaryFull := int64(48), int64(12)
	zero.Usage.CachedInputTokens = &cachedZero
	zero.Usage.UncachedInputTokens = &uncachedFull
	zero.Usage.ReasoningTokens = &reasoningZero
	zero.Usage.OrdinaryOutputTokens = &ordinaryFull
	usage = RunUsage{OpenCodeInvocations: []OpenCodeUsageEntry{zero}, controllerOrder: []string{zero.InvocationID}}
	observations, _, err = evidenceTokenObservations(usage)
	if err != nil {
		t.Fatal(err)
	}
	want = map[string]string{"uncached_input_tokens": "48", "cached_input_tokens": "0", "ordinary_output_tokens": "12", "reasoning_output_tokens": "0"}
	if got := feedbackCostsText(observations[0].Costs); !reflect.DeepEqual(got, want) {
		t.Fatal("known zero must remain known zero", got)
	}

	unknown := openCodeFeedbackEntry(strings.Repeat("d", 64))
	unknown.Usage.CachedInputTokens = nil
	unknown.Usage.UncachedInputTokens = nil
	unknown.Usage.ReasoningTokens = nil
	unknown.Usage.OrdinaryOutputTokens = nil
	usage = RunUsage{OpenCodeInvocations: []OpenCodeUsageEntry{unknown}, controllerOrder: []string{unknown.InvocationID}}
	observations, _, err = evidenceTokenObservations(usage)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations[0].Costs) != 0 {
		t.Fatal("unknown optional partitions must stay null, never zero", observations[0].Costs)
	}
}

func TestOpenCodeFeedbackPartialOptionalCategories(t *testing.T) {
	entry := openCodeFeedbackEntry(strings.Repeat("b", 64))
	entry.Usage.CachedInputTokens = nil
	entry.Usage.UncachedInputTokens = nil
	usage := RunUsage{OpenCodeInvocations: []OpenCodeUsageEntry{entry}, controllerOrder: []string{entry.InvocationID}}
	observations, unavailable, err := evidenceTokenObservations(usage)
	if err != nil || len(unavailable) != 0 {
		t.Fatal("partial entry must contribute known axes", err, unavailable)
	}
	want := map[string]string{"ordinary_output_tokens": "8", "reasoning_output_tokens": "4"}
	if got := feedbackCostsText(observations[0].Costs); !reflect.DeepEqual(got, want) {
		t.Fatal("unknown cached must leave both input components unknown", got)
	}

	entry = openCodeFeedbackEntry(strings.Repeat("c", 64))
	entry.Usage.ReasoningTokens = nil
	entry.Usage.OrdinaryOutputTokens = nil
	usage = RunUsage{OpenCodeInvocations: []OpenCodeUsageEntry{entry}, controllerOrder: []string{entry.InvocationID}}
	observations, _, err = evidenceTokenObservations(usage)
	if err != nil {
		t.Fatal(err)
	}
	want = map[string]string{"uncached_input_tokens": "32", "cached_input_tokens": "16"}
	if got := feedbackCostsText(observations[0].Costs); !reflect.DeepEqual(got, want) {
		t.Fatal("unknown reasoning must leave both output components unknown", got)
	}
}

func TestOpenCodeFeedbackRejectsInvalidSubsetsAndDerived(t *testing.T) {
	base := func() OpenCodeUsageEntry { return openCodeFeedbackEntry(strings.Repeat("b", 64)) }
	negative := int64(-1)
	negativeCount := -1
	cases := []func(*OpenCodeUsageEntry){
		func(e *OpenCodeUsageEntry) { e.Usage.InputTokens = -1 },
		func(e *OpenCodeUsageEntry) { e.Usage.OutputTokens = -1 },
		func(e *OpenCodeUsageEntry) { e.Usage.CachedInputTokens = &negative },
		func(e *OpenCodeUsageEntry) { e.Usage.ReasoningTokens = &negative },
		func(e *OpenCodeUsageEntry) { over := int64(49); e.Usage.CachedInputTokens = &over },
		func(e *OpenCodeUsageEntry) { over := int64(13); e.Usage.ReasoningTokens = &over },
		func(e *OpenCodeUsageEntry) { drifted := int64(31); e.Usage.UncachedInputTokens = &drifted },
		func(e *OpenCodeUsageEntry) { drifted := int64(9); e.Usage.OrdinaryOutputTokens = &drifted },
		func(e *OpenCodeUsageEntry) {
			e.Usage.CachedInputTokens = nil
			kept := int64(32)
			e.Usage.UncachedInputTokens = &kept
		},
		func(e *OpenCodeUsageEntry) {
			e.Usage.ReasoningTokens = nil
			kept := int64(8)
			e.Usage.OrdinaryOutputTokens = &kept
		},
		func(e *OpenCodeUsageEntry) { e.GatewayPending = &negativeCount },
		func(e *OpenCodeUsageEntry) { *e.GatewayCalls = -1 },
		func(e *OpenCodeUsageEntry) { *e.GatewayReceipts = -1 },
	}
	for index, change := range cases {
		entry := base()
		change(&entry)
		usage := RunUsage{OpenCodeInvocations: []OpenCodeUsageEntry{entry}, controllerOrder: []string{entry.InvocationID}}
		if _, _, err := evidenceTokenObservations(usage); err == nil {
			t.Fatalf("invalid OpenCode usage accepted case %d", index)
		}
	}
}

func TestOpenCodeFeedbackRetainsPendingMissingUnmatchedAsUnknown(t *testing.T) {
	base := func() OpenCodeUsageEntry { return openCodeFeedbackEntry(strings.Repeat("b", 64)) }
	pending := 1
	cases := []func(*OpenCodeUsageEntry){
		func(e *OpenCodeUsageEntry) { e.JournalPresent = false },
		func(e *OpenCodeUsageEntry) { e.ReceiptMatched = false },
		func(e *OpenCodeUsageEntry) { e.Usage = nil },
		func(e *OpenCodeUsageEntry) { e.Status = "UNKNOWN" },
		func(e *OpenCodeUsageEntry) { e.Status = "" },
		func(e *OpenCodeUsageEntry) { e.GatewayPending = &pending },
		func(e *OpenCodeUsageEntry) { e.GatewayPending = nil },
		func(e *OpenCodeUsageEntry) { e.GatewayCalls = nil },
		func(e *OpenCodeUsageEntry) { e.GatewayReceipts = nil },
		func(e *OpenCodeUsageEntry) { *e.GatewayCalls = 0 },
		func(e *OpenCodeUsageEntry) { *e.GatewayReceipts = 0 },
		func(e *OpenCodeUsageEntry) { *e.GatewayReceipts = *e.GatewayCalls + 1 },
	}
	for index, change := range cases {
		entry := base()
		change(&entry)
		usage := RunUsage{OpenCodeInvocations: []OpenCodeUsageEntry{entry}, controllerOrder: []string{entry.InvocationID}}
		observations, unavailable, err := evidenceTokenObservations(usage)
		if err != nil || len(observations) != 1 || len(unavailable) != 1 || len(observations[0].Costs) != 0 {
			t.Fatalf("unavailable OpenCode entry became priced case %d: %v %v", index, err, observations)
		}
	}
}

func TestOpenCodeFeedbackMixedRouteOrdering(t *testing.T) {
	codexID, openCodeID := strings.Repeat("a", 64), strings.Repeat("b", 64)
	codex := tokenFeedbackEntry()
	codex.InvocationID = codexID
	usage := RunUsage{Invocations: []RuntimeUsageEntry{codex}, OpenCodeInvocations: []OpenCodeUsageEntry{openCodeFeedbackEntry(openCodeID)}}
	usage.controllerOrder = []string{openCodeID, codexID}
	observations, unavailable, err := evidenceTokenObservations(usage)
	if err != nil || len(unavailable) != 0 || len(observations) != 2 {
		t.Fatal("mixed routes must both contribute", err, unavailable)
	}
	if observations[0].InvocationID != openCodeID || observations[1].InvocationID != codexID {
		t.Fatal("observations bypassed actual controller order", observations[0].InvocationID, observations[1].InvocationID)
	}
	usage.controllerOrder = []string{codexID, openCodeID}
	observations, _, err = evidenceTokenObservations(usage)
	if err != nil || observations[0].InvocationID != codexID || observations[1].InvocationID != openCodeID {
		t.Fatal("observations bypassed reversed controller order", err)
	}

	usage.controllerOrder = nil
	if _, _, err := evidenceTokenObservations(usage); err == nil || !strings.Contains(err.Error(), "feedback order unavailable") {
		t.Fatalf("mixed routes without derived order must fail closed, got %v", err)
	}
	usage.controllerOrder = []string{codexID}
	if _, _, err := evidenceTokenObservations(usage); err == nil || !strings.Contains(err.Error(), "feedback order incomplete") {
		t.Fatalf("incomplete derived order must fail closed, got %v", err)
	}
}

func TestOpenCodeFeedbackRejectsCrossRouteDuplicates(t *testing.T) {
	id := strings.Repeat("a", 64)
	usage := RunUsage{Invocations: []RuntimeUsageEntry{tokenFeedbackEntry()}, OpenCodeInvocations: []OpenCodeUsageEntry{openCodeFeedbackEntry(id)}}
	usage.controllerOrder = []string{id, id}
	if _, _, err := evidenceTokenObservations(usage); err == nil || !strings.Contains(err.Error(), "duplicate invocation identity") {
		t.Fatalf("cross-route duplicate invocation admitted, got %v", err)
	}
}

func TestOpenCodeFeedbackRejectsExcessiveObservations(t *testing.T) {
	usage := RunUsage{}
	ids := make([]string, 0, evidencevalue.MaxObservations+1)
	for i := 0; i <= evidencevalue.MaxObservations; i++ {
		id := fmt.Sprintf("%064x", i+1)
		entry := tokenFeedbackEntry()
		entry.InvocationID = id
		usage.Invocations = append(usage.Invocations, entry)
		ids = append(ids, id)
	}
	usage.OpenCodeInvocations = []OpenCodeUsageEntry{openCodeFeedbackEntry(strings.Repeat("f", 64))}
	usage.controllerOrder = append(ids, strings.Repeat("f", 64))
	if _, _, err := evidenceTokenObservations(usage); err == nil || !strings.Contains(err.Error(), "exceed bounds") {
		t.Fatalf("excessive observations admitted, got %v", err)
	}
	bounded := RunUsage{}
	for i := 0; i < evidencevalue.MaxObservations; i++ {
		id := fmt.Sprintf("%064x", i+1)
		entry := tokenFeedbackEntry()
		entry.InvocationID = id
		bounded.Invocations = append(bounded.Invocations, entry)
	}
	if _, _, err := evidenceTokenObservations(bounded); err != nil {
		t.Fatal("bounded Codex-only observations must remain admitted", err)
	}
}

func feedbackModelForFixture(t *testing.T, path string) evidencevalue.FeedbackRequest {
	t.Helper()
	s, head, err := InspectWithHead(path)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := EvidenceBinding(s, head)
	if err != nil {
		t.Fatal(err)
	}
	resources := []evidencevalue.Resource{}
	for _, name := range []string{"uncached_input_tokens", "cached_input_tokens", "ordinary_output_tokens", "reasoning_output_tokens"} {
		resources = append(resources, evidencevalue.Resource{Name: name, Limit: 1000000})
	}
	allocations := map[string]evidencevalue.Numeric{}
	for _, name := range []string{"uncached_input_tokens", "cached_input_tokens", "ordinary_output_tokens", "reasoning_output_tokens"} {
		allocations[name] = evidencevalue.Numeric("1000")
	}
	return evidencevalue.FeedbackRequest{Model: evidencevalue.EncodeRequest(evidencevalue.Request{Version: 1, Binding: binding, Resources: resources}), Allocations: allocations, Consumed: map[string][]string{}}
}

func TestRepriceEvidenceResourcesReachesOpenCodeCosts(t *testing.T) {
	fixture := buildPlannerPositiveFixture(t, "opencode-feedback-positive", 48, 16, 12, 4)
	beforeJournal, err := os.ReadFile(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	request := feedbackModelForFixture(t, fixture.controllerPath)
	beforeRequest, err := canonical.Bytes(request)
	if err != nil {
		t.Fatal(err)
	}
	report, err := RepriceEvidenceResources(fixture.controllerPath, request)
	if err != nil {
		t.Fatal(err)
	}
	if report.Scope != scopeCodexAndOpenCodeTypedTokens {
		t.Fatal("OpenCode basis must use the extended scope", report.Scope)
	}
	if report.Provenance != "observed_token_feedback_with_caller_supplied_advisory_baseline_and_allocations" {
		t.Fatal("provenance changed", report.Provenance)
	}
	if len(report.Observations) != 1 || len(report.UnavailableInvocations) != 0 {
		t.Fatal("positive fixture must yield one OpenCode observation", report.Observations, report.UnavailableInvocations)
	}
	want := map[string]string{"uncached_input_tokens": "32", "cached_input_tokens": "16", "ordinary_output_tokens": "8", "reasoning_output_tokens": "4"}
	if got := feedbackCostsText(report.Observations[0].Costs); !reflect.DeepEqual(got, want) {
		t.Fatal("fixture costs mispriced", got)
	}
	usage, err := MeasureRunUsage(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if usage.RunID == "" || usage.ControllerHead == "" {
		t.Fatal("usage basis lacks run/head identity")
	}
	basis, err := canonical.Hash("harness.evidence-resource-usage.v1", usage)
	if err != nil || basis != report.UsageHash {
		t.Fatal("usage basis hash mismatch", basis, report.UsageHash, err)
	}
	if report.Observations[0].InvocationID != fixture.invocation.ID {
		t.Fatal("observation lost invocation identity", report.Observations[0].InvocationID)
	}
	if len(report.Feedback.Applied["uncached_input_tokens"]) != 1 || len(report.Feedback.Applied["cached_input_tokens"]) != 1 || len(report.Feedback.Applied["ordinary_output_tokens"]) != 1 || len(report.Feedback.Applied["reasoning_output_tokens"]) != 1 {
		t.Fatal("partial axes failed to advance known cursors", report.Feedback.Applied)
	}
	model, err := report.Feedback.Request.Model.Decode()
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]float64{}
	for _, resource := range model.Resources {
		used[resource.Name] = resource.Used
		if resource.Limit != 1000000 {
			t.Fatal("advisory limit changed", resource)
		}
	}
	if used["uncached_input_tokens"] != 32 || used["cached_input_tokens"] != 16 || used["ordinary_output_tokens"] != 8 || used["reasoning_output_tokens"] != 4 {
		t.Fatal("advisory used quantities missed actual costs", used)
	}
	if report.Feedback.Request.Model.Binding.RunID == "" || report.Feedback.Request.Model.Binding.JournalHead == "" {
		t.Fatal("reusable request lost exact binding")
	}
	afterJournal, err := os.ReadFile(fixture.controllerPath)
	if err != nil || string(beforeJournal) != string(afterJournal) {
		t.Fatal("read-only feedback mutated the controller journal")
	}
	afterRequest, err := canonical.Bytes(request)
	if err != nil || string(beforeRequest) != string(afterRequest) {
		t.Fatal("feedback mutated caller-supplied baselines")
	}
}

func TestRepriceEvidenceResourcesIdempotentReplayAndStaleBinding(t *testing.T) {
	fixture := buildPlannerPositiveFixture(t, "opencode-feedback-replay", 48, 16, 12, 4)
	request := feedbackModelForFixture(t, fixture.controllerPath)
	report, err := RepriceEvidenceResources(fixture.controllerPath, request)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := RepriceEvidenceResources(fixture.controllerPath, report.Feedback.Request)
	if err != nil {
		t.Fatal("reusable request must reprice", err)
	}
	for axis, applied := range replayed.Feedback.Applied {
		if len(applied) != 0 {
			t.Fatal("replay priced an axis twice", axis, applied)
		}
	}
	first, err := canonical.Bytes(report.Feedback.Request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonical.Bytes(replayed.Feedback.Request)
	if err != nil || string(first) != string(second) {
		t.Fatal("idempotent replay changed advisory state")
	}
	stale := request
	stale.Model.Binding.JournalHead = strings.Repeat("0", 64)
	if _, err := RepriceEvidenceResources(fixture.controllerPath, stale); err == nil || !strings.Contains(err.Error(), "stale or substituted") {
		t.Fatalf("stale request admitted, got %v", err)
	}
	before, err := journal.Read(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RepriceEvidenceResources(fixture.controllerPath, stale); err == nil {
		t.Fatal("stale request admitted on retry")
	}
	after, err := journal.Read(fixture.controllerPath)
	if err != nil || len(before) != len(after) {
		t.Fatal("rejected feedback mutated the journal")
	}
}

func TestRepriceEvidenceResourcesWithSchedulerPreservesV1(t *testing.T) {
	fixture := buildPlannerPositiveFixture(t, "opencode-feedback-scheduler", 48, 16, 12, 4)
	request := feedbackModelForFixture(t, fixture.controllerPath)
	plain, err := RepriceEvidenceResources(fixture.controllerPath, request)
	if err != nil {
		t.Fatal(err)
	}
	threaded, err := RepriceEvidenceResourcesWithScheduler(fixture.controllerPath, request, "")
	if err != nil {
		t.Fatal(err)
	}
	unusedPath := fixture.controllerPath + ".unused-scheduler"
	if err := os.WriteFile(unusedPath, []byte("unused"), 0600); err != nil {
		t.Fatal(err)
	}
	ignored, err := RepriceEvidenceResourcesWithScheduler(fixture.controllerPath, request, unusedPath)
	if err != nil {
		t.Fatal("v1 turns must not consult the scheduler", err)
	}
	for _, report := range []EvidenceFeedbackReport{threaded, ignored} {
		threadedBytes, err := canonical.Bytes(report)
		if err != nil {
			t.Fatal(err)
		}
		plainBytes, err := canonical.Bytes(plain)
		if err != nil || string(threadedBytes) != string(plainBytes) {
			t.Fatal("scheduler seam changed v1 feedback")
		}
	}
}

func feedbackOrderingInvocation(t *testing.T, profile runtime.Profile, input string) runtime.Invocation {
	t.Helper()
	invocation, err := runtime.NewInvocation(profile, input)
	if err != nil {
		t.Fatal(err)
	}
	return invocation
}

func feedbackOrderingEvent(t *testing.T, kind string, value any) journal.Event {
	t.Helper()
	raw, err := canonical.Bytes(value)
	if err != nil {
		t.Fatal(err)
	}
	return journal.Event{Kind: kind, Payload: raw}
}

// TestFeedbackOrderDerivesFromControllerEvents is a helper regression over
// the private ordering seam: positions come from actual canonical journal
// event payloads in shared event-index space, never from a manually
// supplied observation order. It covers interleaved Codex host positions,
// a completed OpenCode receipt without dispatch (receipt-index fallback)
// and dispatch-admitted pending/completed positions, in both route group
// ordering directions, then demonstrates the expected sequential feedback
// observation order through evidenceTokenObservations and ApplyFeedback.
func TestFeedbackOrderDerivesFromControllerEvents(t *testing.T) {
	runID := strings.Repeat("0", 64)
	codexFirst := feedbackOrderingInvocation(t, runtime.Profile{Runtime: "codex-cli", Provider: "fixture", Model: "model", Effort: "high", Role: "explorer"}, "First bounded exploration.")
	codexLast := feedbackOrderingInvocation(t, runtime.Profile{Runtime: "codex-cli", Provider: "fixture", Model: "model", Effort: "high", Role: "writer"}, "Second bounded change.")
	openDispatched := feedbackOrderingInvocation(t, runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "high", Role: "writer"}, "Dispatched bounded change.")
	openLegacy := feedbackOrderingInvocation(t, runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "high", Role: "reviewer"}, "Legacy bounded review.")
	openPending := feedbackOrderingInvocation(t, runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "high", Role: "reviewer"}, "Pending bounded review.")
	launch := codexhost.Launch{Version: 1, Root: t.TempDir(), Binary: "codex", BinaryHash: strings.Repeat("e", 64), ConfigHash: strings.Repeat("f", 64)}
	receiptFor := func(invocation runtime.Invocation, role string) providerDispatchReceipt {
		return providerDispatchReceipt{Version: 1, Role: role, InvocationID: invocation.ID, Result: runtime.Result{Requested: invocation.Profile}}
	}
	admissionFor := func(invocation runtime.Invocation) AgentDispatchAdmission {
		return AgentDispatchAdmission{Version: 1, RunID: runID, TreeID: runID, Invocation: invocation}
	}
	events := []journal.Event{
		feedbackOrderingEvent(t, "explorer.host-intent", ExplorerHostIntent{Question: "First bounded exploration.", Invocation: codexFirst, Launch: launch}),
		feedbackOrderingEvent(t, "agent.dispatch-admitted", admissionFor(openDispatched)),
		feedbackOrderingEvent(t, "role.provider-observed", receiptFor(openDispatched, "writer")),
		feedbackOrderingEvent(t, "role.provider-observed", receiptFor(openLegacy, "reviewer")),
		feedbackOrderingEvent(t, "agent.dispatch-admitted", admissionFor(openPending)),
		feedbackOrderingEvent(t, "writer.host-intent", WriterHostIntent{Invocation: codexLast, Launch: launch}),
	}
	positions := openCodeFeedbackPositions(events)
	wantPositions := map[string]int{openDispatched.ID: 1, openLegacy.ID: 3, openPending.ID: 4}
	if !reflect.DeepEqual(positions, wantPositions) {
		t.Fatal("controller positions bypassed journal events", positions)
	}
	targets := []usageTarget{{invocation: codexFirst, order: 0}, {invocation: codexLast, order: 5}}
	merged := mergeFeedbackControllerOrder(targets, positions)
	wantOrder := []string{codexFirst.ID, openDispatched.ID, openLegacy.ID, openPending.ID, codexLast.ID}
	if !reflect.DeepEqual(merged, wantOrder) {
		t.Fatal("merged order is not controller-event-derived", merged)
	}
	first := tokenFeedbackEntry()
	first.InvocationID = codexFirst.ID
	last := tokenFeedbackEntry()
	last.InvocationID = codexLast.ID
	pending := openCodeFeedbackEntry(openPending.ID)
	pending.Status = "UNKNOWN"
	pending.ReceiptMatched = false
	pending.Usage = nil
	usage := RunUsage{
		Invocations:         []RuntimeUsageEntry{first, last},
		OpenCodeInvocations: []OpenCodeUsageEntry{openCodeFeedbackEntry(openDispatched.ID), openCodeFeedbackEntry(openLegacy.ID), pending},
		controllerOrder:     merged,
	}
	observations, unavailable, err := evidenceTokenObservations(usage)
	if err != nil {
		t.Fatal(err)
	}
	gotOrder := make([]string, 0, len(observations))
	for _, observation := range observations {
		gotOrder = append(gotOrder, observation.InvocationID)
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatal("mixed-route observations bypassed controller order", gotOrder)
	}
	if !reflect.DeepEqual(unavailable, []string{openPending.ID}) {
		t.Fatal("pending OpenCode entry left the unavailable set", unavailable)
	}
	resources := []evidencevalue.Resource{}
	allocations := map[string]evidencevalue.Numeric{}
	for _, name := range []string{"uncached_input_tokens", "cached_input_tokens", "ordinary_output_tokens", "reasoning_output_tokens"} {
		resources = append(resources, evidencevalue.Resource{Name: name, Limit: 1000000})
		allocations[name] = evidencevalue.Numeric("1000")
	}
	feedback, err := evidencevalue.ApplyFeedback(evidencevalue.FeedbackRequest{Model: evidencevalue.EncodeRequest(evidencevalue.Request{Version: 1, Resources: resources}), Allocations: allocations}, observations)
	if err != nil {
		t.Fatal(err)
	}
	wantApplied := []string{codexFirst.ID, openDispatched.ID, openLegacy.ID, codexLast.ID}
	for _, axis := range []string{"uncached_input_tokens", "cached_input_tokens", "ordinary_output_tokens", "reasoning_output_tokens"} {
		if !reflect.DeepEqual(feedback.Applied[axis], wantApplied) {
			t.Fatal("sequential feedback bypassed controller order", axis, feedback.Applied[axis])
		}
	}
	if len(feedback.Skipped) != 0 {
		t.Fatal("known axes skipped after ordered observations", feedback.Skipped)
	}

	// Reverse direction: the OpenCode admission precedes the Codex host intent.
	reverse := []journal.Event{
		feedbackOrderingEvent(t, "agent.dispatch-admitted", admissionFor(openDispatched)),
		feedbackOrderingEvent(t, "role.provider-observed", receiptFor(openDispatched, "writer")),
		feedbackOrderingEvent(t, "explorer.host-intent", ExplorerHostIntent{Question: "Later bounded exploration.", Invocation: codexFirst, Launch: launch}),
	}
	reverseOrder := mergeFeedbackControllerOrder([]usageTarget{{invocation: codexFirst, order: 2}}, openCodeFeedbackPositions(reverse))
	if !reflect.DeepEqual(reverseOrder, []string{openDispatched.ID, codexFirst.ID}) {
		t.Fatal("reverse controller order mismerged", reverseOrder)
	}
	reverseUsage := RunUsage{
		Invocations:         []RuntimeUsageEntry{first},
		OpenCodeInvocations: []OpenCodeUsageEntry{openCodeFeedbackEntry(openDispatched.ID)},
		controllerOrder:     reverseOrder,
	}
	reverseObservations, _, err := evidenceTokenObservations(reverseUsage)
	if err != nil || len(reverseObservations) != 2 || reverseObservations[0].InvocationID != openDispatched.ID || reverseObservations[1].InvocationID != codexFirst.ID {
		t.Fatal("reverse mixed-route observations bypassed controller order", reverseObservations, err)
	}
}
