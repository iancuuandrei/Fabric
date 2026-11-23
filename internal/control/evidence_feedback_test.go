package control

import (
	"reflect"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/codexusage"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func tokenFeedbackEntry() RuntimeUsageEntry {
	return RuntimeUsageEntry{InvocationID: strings.Repeat("a", 64), JournalPresent: true, ReceiptMatched: true, Usage: &codexruntime.ContextUsage{Completed: true, ProviderUsage: runtime.Usage{Accounting: &codexusage.Receipt{Source: codexusage.SourceCodexAppServer, Coverage: codexusage.CoverageObserved, Delta: codexusage.TokenUsage{InputTokens: 100, CachedInputTokens: 60, OutputTokens: 30, ReasoningOutputTokens: 10, TotalTokens: 130}}}}}
}

func TestEvidenceTokenFeedbackSplitsSubsetsWithoutDoubleCounting(t *testing.T) {
	observations, unavailable, err := evidenceTokenObservations(RunUsage{Invocations: []RuntimeUsageEntry{tokenFeedbackEntry()}})
	if err != nil || len(unavailable) != 0 || len(observations) != 1 {
		t.Fatal(err)
	}
	values := map[string]string{}
	for name, cost := range observations[0].Costs {
		if cost != nil {
			values[name] = string(*cost)
		}
	}
	want := map[string]string{"uncached_input_tokens": "40", "cached_input_tokens": "60", "ordinary_output_tokens": "20", "reasoning_output_tokens": "10"}
	if !reflect.DeepEqual(values, want) {
		t.Fatal("token subsets charged twice", values)
	}
	if observations[0].Costs["money_minor_units"] != nil || observations[0].Costs["wall_ms"] != nil {
		t.Fatal("unavailable money/time invented")
	}
}

func TestEvidenceTokenFeedbackRetainsMissingAndUnmatchedAsUnknown(t *testing.T) {
	for _, change := range []func(*RuntimeUsageEntry){
		func(e *RuntimeUsageEntry) { e.ReceiptMatched = false },
		func(e *RuntimeUsageEntry) { e.JournalPresent = false },
		func(e *RuntimeUsageEntry) { e.Usage = nil },
		func(e *RuntimeUsageEntry) { e.Usage.Completed = false },
		func(e *RuntimeUsageEntry) { e.Usage.PendingCalls = 1 },
		func(e *RuntimeUsageEntry) { e.Usage.ProviderUsage.Accounting = nil },
		func(e *RuntimeUsageEntry) { e.Usage.ProviderUsage.Accounting.Coverage = codexusage.CoverageUnknown },
		func(e *RuntimeUsageEntry) { e.Usage.ProviderUsage.Accounting.Anomalies = []string{"sticky anomaly"} },
		func(e *RuntimeUsageEntry) { e.Usage.ProviderUsage.Accounting.Source = "other" },
	} {
		entry := tokenFeedbackEntry()
		change(&entry)
		observations, unavailable, err := evidenceTokenObservations(RunUsage{Invocations: []RuntimeUsageEntry{entry}})
		if err != nil || len(observations) != 1 || len(unavailable) != 1 || len(observations[0].Costs) != 0 {
			t.Fatal("unavailable usage became zero or priced", err)
		}
	}
	entry := tokenFeedbackEntry()
	entry.Usage.ProviderUsage.Accounting.Delta.CachedInputTokens = 101
	if _, _, err := evidenceTokenObservations(RunUsage{Invocations: []RuntimeUsageEntry{entry}}); err == nil {
		t.Fatal("invalid subset accepted")
	}
}
