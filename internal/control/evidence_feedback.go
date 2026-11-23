package control

import (
	"errors"
	"sort"
	"strconv"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexusage"
	"github.com/iancuuandrei/fabric/internal/evidencevalue"
)

// Evidence feedback scopes over the exact receipt-verified token basis. The
// legacy scope covers Codex-only runs; the extended scope covers runs whose
// basis additionally includes admitted OpenCode invocations.
const (
	scopeCodexTypedTokens            = "receipt_matched_completed_codex_typed_tokens"
	scopeCodexAndOpenCodeTypedTokens = "receipt_matched_completed_codex_and_opencode_typed_tokens"
)

// EvidenceFeedbackReport binds advisory price updates to receipt-verified usage.
// No run budgets, effects, policies, acceptance or journals are modified.
type EvidenceFeedbackReport struct {
	Provenance             string                              `json:"provenance"`
	UsageHash              string                              `json:"usage_hash"`
	Scope                  string                              `json:"scope"`
	Feedback               evidencevalue.FeedbackResult        `json:"feedback"`
	Observations           []evidencevalue.ResourceObservation `json:"observations"`
	UnavailableInvocations []string                            `json:"unavailable_invocations"`
}

// openCodeFeedbackCosts projects one admitted OpenCode token usage into the
// four advisory token axes without charging parent totals again and without
// cache-write or new resource axes. Unknown optional cached input leaves both
// input components unknown; unknown optional reasoning leaves both output
// components unknown. Known zero remains known zero. Money, time, compute and
// slots are never derived. ok=false keeps the entry unavailable with no
// invented costs; invalid numeric values are rejected.
func openCodeFeedbackCosts(entry OpenCodeUsageEntry) (map[string]*evidencevalue.Numeric, bool, error) {
	costs := map[string]*evidencevalue.Numeric{}
	if !entry.JournalPresent || !entry.ReceiptMatched || entry.Usage == nil || entry.Status != "completed" {
		return costs, false, nil
	}
	if entry.GatewayPending == nil || *entry.GatewayPending != 0 {
		if entry.GatewayPending != nil && *entry.GatewayPending < 0 {
			return nil, false, errors.New("observed opencode gateway counts invalid")
		}
		return costs, false, nil
	}
	if entry.GatewayCalls == nil || entry.GatewayReceipts == nil {
		return costs, false, nil
	}
	if *entry.GatewayCalls < 0 || *entry.GatewayReceipts < 0 {
		return nil, false, errors.New("observed opencode gateway counts invalid")
	}
	if *entry.GatewayCalls < 1 || *entry.GatewayReceipts < 1 || *entry.GatewayReceipts > *entry.GatewayCalls {
		return costs, false, nil
	}
	usage := entry.Usage
	if usage.InputTokens < 0 || usage.OutputTokens < 0 {
		return nil, false, errors.New("observed opencode token counts invalid")
	}
	if usage.CachedInputTokens == nil {
		if usage.UncachedInputTokens != nil {
			return nil, false, errors.New("observed opencode token subsets invalid")
		}
	} else {
		cached := *usage.CachedInputTokens
		if cached < 0 || cached > usage.InputTokens {
			return nil, false, errors.New("observed opencode token subsets invalid")
		}
		if usage.UncachedInputTokens != nil && *usage.UncachedInputTokens != usage.InputTokens-cached {
			return nil, false, errors.New("observed opencode token subsets invalid")
		}
		uncached := evidencevalue.Numeric(strconv.FormatInt(usage.InputTokens-cached, 10))
		cachedValue := evidencevalue.Numeric(strconv.FormatInt(cached, 10))
		costs["uncached_input_tokens"] = &uncached
		costs["cached_input_tokens"] = &cachedValue
	}
	if usage.ReasoningTokens == nil {
		if usage.OrdinaryOutputTokens != nil {
			return nil, false, errors.New("observed opencode token subsets invalid")
		}
	} else {
		reasoning := *usage.ReasoningTokens
		if reasoning < 0 || reasoning > usage.OutputTokens {
			return nil, false, errors.New("observed opencode token subsets invalid")
		}
		if usage.OrdinaryOutputTokens != nil && *usage.OrdinaryOutputTokens != usage.OutputTokens-reasoning {
			return nil, false, errors.New("observed opencode token subsets invalid")
		}
		ordinary := evidencevalue.Numeric(strconv.FormatInt(usage.OutputTokens-reasoning, 10))
		reasoningValue := evidencevalue.Numeric(strconv.FormatInt(reasoning, 10))
		costs["ordinary_output_tokens"] = &ordinary
		costs["reasoning_output_tokens"] = &reasoningValue
	}
	return costs, true, nil
}

func evidenceTokenObservations(usage RunUsage) ([]evidencevalue.ResourceObservation, []string, error) {
	observations := []evidencevalue.ResourceObservation{}
	unavailable := []string{}
	seen := map[string]bool{}
	for _, entry := range usage.Invocations {
		seen[entry.InvocationID] = true
		observation := evidencevalue.ResourceObservation{InvocationID: entry.InvocationID, Costs: map[string]*evidencevalue.Numeric{}}
		observations = append(observations, observation)
		if !entry.JournalPresent || !entry.ReceiptMatched || entry.Usage == nil || !entry.Usage.Completed || entry.Usage.PendingCalls != 0 {
			unavailable = append(unavailable, entry.InvocationID)
			continue
		}
		accounting := entry.Usage.ProviderUsage.Accounting
		if accounting == nil || accounting.Source != codexusage.SourceCodexAppServer || accounting.Coverage != codexusage.CoverageObserved || len(accounting.Anomalies) != 0 {
			unavailable = append(unavailable, entry.InvocationID)
			continue
		}
		tokens := accounting.Delta
		if tokens.InputTokens < 0 || tokens.CachedInputTokens < 0 || tokens.CachedInputTokens > tokens.InputTokens || tokens.OutputTokens < 0 || tokens.ReasoningOutputTokens < 0 || tokens.ReasoningOutputTokens > tokens.OutputTokens {
			return nil, nil, errors.New("observed token resource subsets invalid")
		}
		for name, count := range map[string]int64{
			"uncached_input_tokens":   tokens.InputTokens - tokens.CachedInputTokens,
			"cached_input_tokens":     tokens.CachedInputTokens,
			"ordinary_output_tokens":  tokens.OutputTokens - tokens.ReasoningOutputTokens,
			"reasoning_output_tokens": tokens.ReasoningOutputTokens,
		} {
			value := evidencevalue.Numeric(strconv.FormatInt(count, 10))
			observation.Costs[name] = &value
		}
	}
	for _, entry := range usage.OpenCodeInvocations {
		if seen[entry.InvocationID] {
			return nil, nil, errors.New("duplicate invocation identity across route groups")
		}
		seen[entry.InvocationID] = true
		costs, ok, err := openCodeFeedbackCosts(entry)
		if err != nil {
			return nil, nil, err
		}
		observation := evidencevalue.ResourceObservation{InvocationID: entry.InvocationID, Costs: map[string]*evidencevalue.Numeric{}}
		if !ok {
			observations = append(observations, observation)
			unavailable = append(unavailable, entry.InvocationID)
			continue
		}
		observation.Costs = costs
		observations = append(observations, observation)
	}
	if len(observations) > evidencevalue.MaxObservations {
		return nil, nil, errors.New("resource feedback observations exceed bounds")
	}
	if len(usage.OpenCodeInvocations) > 0 {
		if len(usage.controllerOrder) == 0 {
			return nil, nil, errors.New("opencode feedback order unavailable")
		}
		rank := make(map[string]int, len(usage.controllerOrder))
		for index, id := range usage.controllerOrder {
			if _, duplicate := rank[id]; !duplicate {
				rank[id] = index
			}
		}
		for _, observation := range observations {
			if _, ok := rank[observation.InvocationID]; !ok {
				return nil, nil, errors.New("opencode feedback order incomplete")
			}
		}
		sort.SliceStable(observations, func(i, j int) bool {
			return rank[observations[i].InvocationID] < rank[observations[j].InvocationID]
		})
	}
	return observations, unavailable, nil
}

// RepriceEvidenceResources derives advisory token feedback from exact runtime
// receipts. The supplied model must bind the same inspected controller prefix.
// Missing typed accounting never becomes zero; non-token axes remain unknown.
func RepriceEvidenceResources(path string, request evidencevalue.FeedbackRequest) (EvidenceFeedbackReport, error) {
	return RepriceEvidenceResourcesWithScheduler(path, request, "")
}

// RepriceEvidenceResourcesWithScheduler is RepriceEvidenceResources with an
// exact scheduler journal binding for composite turns. The scheduler is
// consulted only through the existing journal-only composite verifier for v3
// turns; all other turns never read it.
func RepriceEvidenceResourcesWithScheduler(path string, request evidencevalue.FeedbackRequest, schedulerPath string) (EvidenceFeedbackReport, error) {
	var report EvidenceFeedbackReport
	raw, err := canonical.Bytes(request)
	if err != nil || len(raw) > evidencevalue.MaxBytes {
		return report, errors.New("resource feedback request exceeds encoding bounds")
	}
	s, head, err := InspectWithHead(path)
	if err != nil {
		return report, err
	}
	binding, err := EvidenceBinding(s, head)
	if err != nil {
		return report, err
	}
	if request.Model.Binding != binding {
		return report, errors.New("resource feedback binding is stale or substituted")
	}
	usage, err := MeasureRunUsageWithScheduler(path, schedulerPath)
	if err != nil {
		return report, err
	}
	if usage.RunID != s.RunID || usage.ControllerHead != head {
		return report, errors.New("controller changed during resource feedback inspection")
	}
	report.Observations, report.UnavailableInvocations, err = evidenceTokenObservations(usage)
	if err != nil {
		return report, err
	}
	report.Feedback, err = evidencevalue.ApplyFeedback(request, report.Observations)
	if err != nil {
		return EvidenceFeedbackReport{}, err
	}
	report.UsageHash, err = canonical.Hash("harness.evidence-resource-usage.v1", usage)
	report.Scope = scopeCodexTypedTokens
	if len(usage.OpenCodeInvocations) > 0 {
		report.Scope = scopeCodexAndOpenCodeTypedTokens
	}
	report.Provenance = "observed_token_feedback_with_caller_supplied_advisory_baseline_and_allocations"
	return report, err
}
