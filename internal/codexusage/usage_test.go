package codexusage

import (
	"errors"
	"reflect"
	"testing"
)

func usage(total int64) TokenUsage {
	input := total * 2 / 3
	output := total - input
	return TokenUsage{
		InputTokens:           input,
		CachedInputTokens:     input / 3,
		OutputTokens:          output,
		ReasoningOutputTokens: output / 2,
		TotalTokens:           total,
	}
}

func notification(threadID, turnID string, total TokenUsage) Notification {
	return Notification{ThreadID: threadID, TurnID: turnID, TokenUsage: ThreadTokenUsage{Last: TokenUsage{}, Total: total}}
}

func TestFreshHighWaterUsesCumulativeTotal(t *testing.T) {
	tracker, err := FreshTracker("thread", "turn", nil)
	if err != nil {
		t.Fatal(err)
	}
	if receipt := tracker.Receipt(); receipt.Coverage != CoverageUnknown || receipt.Delta.TotalTokens != 0 {
		t.Fatal("fresh tracker claimed observed coverage", receipt)
	}
	for _, want := range []int64{10, 30} {
		receipt, err := tracker.Observe(notification("thread", "turn", usage(want)))
		if err != nil {
			t.Fatal(err)
		}
		if receipt.Coverage != CoverageObserved || receipt.Before.TotalTokens != 0 || receipt.After.TotalTokens != want || receipt.Delta.TotalTokens != want {
			t.Fatalf("cumulative observation %d produced %+v", want, receipt)
		}
	}
}

func TestExistingBaselineProducesTurnDelta(t *testing.T) {
	tracker, err := NewTracker("thread", "turn", usage(200), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ total, delta int64 }{{220, 20}, {250, 50}} {
		receipt, err := tracker.Observe(notification("thread", "turn", usage(test.total)))
		if err != nil || receipt.After.TotalTokens != test.total || receipt.Delta.TotalTokens != test.delta {
			t.Fatalf("total %d: receipt=%+v err=%v", test.total, receipt, err)
		}
	}
}

func TestDuplicateAndReplayDoNotAddLastOrSubsets(t *testing.T) {
	tracker, _ := FreshTracker("thread", "turn", nil)
	total := TokenUsage{InputTokens: 6, CachedInputTokens: 5, OutputTokens: 4, ReasoningOutputTokens: 3, TotalTokens: 10}
	n := notification("thread", "turn", total)
	n.TokenUsage.Last = TokenUsage{InputTokens: 500, OutputTokens: 499, TotalTokens: 999}
	first, err := tracker.Observe(n)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tracker.Observe(n)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || second.Delta.TotalTokens != 10 {
		t.Fatal("duplicate was added or changed receipt", first, second)
	}
	if second.Delta.InputTokens != 6 || second.Delta.TotalTokens == second.Delta.InputTokens+second.Delta.CachedInputTokens+second.Delta.OutputTokens+second.Delta.ReasoningOutputTokens {
		t.Fatal("component subsets were treated as additive", second.Delta)
	}

	replayed, _ := FreshTracker("thread", "turn", nil)
	if _, err := replayed.Observe(n); err != nil {
		t.Fatal(err)
	}
	got, err := replayed.Observe(n)
	if err != nil || !reflect.DeepEqual(got, second) {
		t.Fatal("replay was not deterministic", got, err)
	}
}

func TestRegressionReturnsReceiptAndRetainsHighWater(t *testing.T) {
	tracker, _ := FreshTracker("thread", "turn", nil)
	high := TokenUsage{InputTokens: 18, CachedInputTokens: 5, OutputTokens: 12, ReasoningOutputTokens: 2, TotalTokens: 30}
	if _, err := tracker.Observe(notification("thread", "turn", high)); err != nil {
		t.Fatal(err)
	}
	lower := TokenUsage{InputTokens: 17, CachedInputTokens: 7, OutputTokens: 12, ReasoningOutputTokens: 2, TotalTokens: 29}
	receipt, err := tracker.Observe(notification("thread", "turn", lower))
	if !errors.Is(err, ErrUsageRegression) {
		t.Fatal("regression not classified", err)
	}
	if receipt.Coverage != CoverageUnknown || receipt.After.InputTokens != 18 || receipt.After.CachedInputTokens != 7 || receipt.After.TotalTokens != 30 {
		t.Fatal("regression subtracted or discarded safe high-water", receipt)
	}
	wantAnomalies := []string{"inputTokens", "totalTokens"}
	if !reflect.DeepEqual(receipt.Anomalies, wantAnomalies) {
		t.Fatal("unexpected anomaly set", receipt.Anomalies)
	}
}

func TestWrongTurnRejectsWithoutMutation(t *testing.T) {
	tracker, _ := FreshTracker("thread", "turn", nil)
	before := tracker.Receipt()
	receipt, err := tracker.Observe(notification("thread", "other", usage(10)))
	if !errors.Is(err, ErrIdentityMismatch) || !reflect.DeepEqual(receipt, before) || !reflect.DeepEqual(tracker.Receipt(), before) {
		t.Fatal("wrong turn changed tracker or lacked classification", receipt, err)
	}
}

func TestBudgetThresholdReportsOvershootWithoutCapping(t *testing.T) {
	limit := int64(100)
	tracker, _ := FreshTracker("thread", "turn", &limit)
	receipt, err := tracker.Observe(notification("thread", "turn", usage(99)))
	if err != nil || receipt.Budget == nil || receipt.Budget.Exhausted || receipt.Budget.Overshoot != 0 || receipt.After.TotalTokens != 99 {
		t.Fatal("99 token budget status", receipt, err)
	}
	receipt, err = tracker.Observe(notification("thread", "turn", usage(105)))
	if err != nil || receipt.Budget == nil || !receipt.Budget.Exhausted || receipt.Budget.Overshoot != 5 || receipt.Budget.Used != 105 || receipt.After.TotalTokens != 105 {
		t.Fatal("overshoot was capped or misreported", receipt, err)
	}
}

func TestMissingOptionalCacheWriteRemainsUnknown(t *testing.T) {
	tracker, _ := FreshTracker("thread", "turn", nil)
	receipt, err := tracker.Observe(notification("thread", "turn", usage(10)))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Before.CacheWriteInputTokens != nil || receipt.After.CacheWriteInputTokens != nil || receipt.Delta.CacheWriteInputTokens != nil {
		t.Fatal("missing optional cache-write count became known", receipt)
	}
}

func TestExactBudgetLimitIsExhausted(t *testing.T) {
	limit := int64(100)
	tracker, _ := FreshTracker("thread", "turn", &limit)
	receipt, err := tracker.Observe(notification("thread", "turn", usage(100)))
	if err != nil || receipt.Budget == nil || !receipt.Budget.Exhausted || receipt.Budget.Overshoot != 0 {
		t.Fatal("exact limit was not exhausted", receipt, err)
	}
}

func TestDecodeNotificationRequiresSchemaFieldsAndPreservesOptionalUnknown(t *testing.T) {
	raw := []byte(`{"threadId":"thread","turnId":"turn","tokenUsage":{"last":{"inputTokens":1,"cachedInputTokens":1,"outputTokens":4,"reasoningOutputTokens":2,"totalTokens":5},"total":{"inputTokens":10,"cachedInputTokens":5,"outputTokens":40,"reasoningOutputTokens":20,"totalTokens":50},"modelContextWindow":null}}`)
	n, err := DecodeNotification(raw)
	if err != nil {
		t.Fatal(err)
	}
	if n.ThreadID != "thread" || n.TurnID != "turn" || n.TokenUsage.Total.TotalTokens != 50 || n.TokenUsage.Total.CacheWriteInputTokens != nil {
		t.Fatal("decoded notification differs from schema", n)
	}

	missing := []byte(`{"threadId":"thread","turnId":"turn","tokenUsage":{"last":{"inputTokens":1,"cachedInputTokens":1,"outputTokens":4,"reasoningOutputTokens":2,"totalTokens":5},"total":{"inputTokens":10,"cachedInputTokens":5,"outputTokens":40,"totalTokens":50}}}`)
	if _, err := DecodeNotification(missing); !errors.Is(err, ErrInvalidUsage) {
		t.Fatal("missing required reasoningOutputTokens was accepted", err)
	}
}

func TestObserveRejectsMalformedComponentRelationships(t *testing.T) {
	tracker, _ := FreshTracker("thread", "turn", nil)
	malformed := []TokenUsage{
		{InputTokens: 4, CachedInputTokens: 5, OutputTokens: 6, TotalTokens: 10},
		{InputTokens: 4, OutputTokens: 6, ReasoningOutputTokens: 7, TotalTokens: 10},
		{InputTokens: 4, OutputTokens: 6, TotalTokens: 11},
	}
	cacheWrite := int64(5)
	malformed = append(malformed, TokenUsage{InputTokens: 4, OutputTokens: 6, TotalTokens: 10, CacheWriteInputTokens: &cacheWrite})
	before := tracker.Receipt()
	for _, counts := range malformed {
		if _, err := tracker.Observe(notification("thread", "turn", counts)); !errors.Is(err, ErrInvalidUsage) {
			t.Fatal("malformed component relationship accepted", counts, err)
		}
	}
	if !reflect.DeepEqual(before, tracker.Receipt()) {
		t.Fatal("invalid observations changed tracker")
	}
}

func TestOmittedCacheWriteDoesNotInheritKnownBaseline(t *testing.T) {
	zero := int64(0)
	tracker, err := NewTracker("thread", "turn", TokenUsage{CacheWriteInputTokens: &zero}, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := tracker.Observe(notification("thread", "turn", usage(30)))
	if err != nil || receipt.Before.CacheWriteInputTokens == nil || receipt.After.CacheWriteInputTokens != nil || receipt.Delta.CacheWriteInputTokens != nil {
		t.Fatal(receipt, err)
	}
	total := usage(40)
	total.CacheWriteInputTokens = &zero
	receipt, err = tracker.Observe(notification("thread", "turn", total))
	if err != nil || receipt.Delta.CacheWriteInputTokens == nil || *receipt.Delta.CacheWriteInputTokens != 0 {
		t.Fatal(receipt, err)
	}
	receipt, err = tracker.Observe(notification("thread", "turn", usage(50)))
	if err != nil || receipt.After.CacheWriteInputTokens != nil || receipt.Delta.CacheWriteInputTokens != nil {
		t.Fatal(receipt, err)
	}
}
