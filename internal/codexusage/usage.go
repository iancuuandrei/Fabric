package codexusage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// SourceCodexAppServer identifies the sole evidence source accepted here.
	SourceCodexAppServer = "CODEX_APP_SERVER"

	CoverageObserved = "OBSERVED"
	CoverageUnknown  = "UNKNOWN"
)

var (
	ErrUninitialized    = errors.New("codex usage tracker is uninitialized")
	ErrIdentityMismatch = errors.New("codex usage notification identity mismatch")
	ErrInvalidUsage     = errors.New("codex usage notification is invalid")
	ErrUsageRegression  = errors.New("codex cumulative token usage regressed")
)

// TokenUsage mirrors the app-server TokenUsageBreakdown. The component subsets
// are independent observations and are never summed to derive TotalTokens.
// CacheWriteInputTokens remains nil when the optional field was not observed.
type TokenUsage struct {
	InputTokens           int64  `json:"inputTokens"`
	CachedInputTokens     int64  `json:"cachedInputTokens"`
	OutputTokens          int64  `json:"outputTokens"`
	ReasoningOutputTokens int64  `json:"reasoningOutputTokens"`
	TotalTokens           int64  `json:"totalTokens"`
	CacheWriteInputTokens *int64 `json:"cacheWriteInputTokens,omitempty"`
}

// ThreadTokenUsage mirrors tokenUsage in thread/tokenUsage/updated params.
// Total is cumulative. Last is validated but is not added to Total.
type ThreadTokenUsage struct {
	Last               TokenUsage `json:"last"`
	Total              TokenUsage `json:"total"`
	ModelContextWindow *int64     `json:"modelContextWindow,omitempty"`
}

// Notification mirrors thread/tokenUsage/updated params.
type Notification struct {
	ThreadID   string           `json:"threadId"`
	TurnID     string           `json:"turnId"`
	TokenUsage ThreadTokenUsage `json:"tokenUsage"`
}

type notificationUsageWire struct {
	InputTokens           *int64 `json:"inputTokens"`
	CachedInputTokens     *int64 `json:"cachedInputTokens"`
	OutputTokens          *int64 `json:"outputTokens"`
	ReasoningOutputTokens *int64 `json:"reasoningOutputTokens"`
	TotalTokens           *int64 `json:"totalTokens"`
	CacheWriteInputTokens *int64 `json:"cacheWriteInputTokens,omitempty"`
}

// DecodeNotification strictly decodes the installed app-server parameter shape.
// In particular, all five schema-required fields in both last and total must be
// present; the optional cache-write field remains nil when absent. Decoding does
// not persist raw evidence and callers must do so before passing the result to Observe.
func DecodeNotification(raw []byte) (Notification, error) {
	type wireThreadUsage struct {
		Last               *notificationUsageWire `json:"last"`
		Total              *notificationUsageWire `json:"total"`
		ModelContextWindow *int64                 `json:"modelContextWindow"`
	}
	type wireNotification struct {
		ThreadID   *string          `json:"threadId"`
		TurnID     *string          `json:"turnId"`
		TokenUsage *wireThreadUsage `json:"tokenUsage"`
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire wireNotification
	if err := decoder.Decode(&wire); err != nil {
		return Notification{}, fmt.Errorf("%w: decode: %v", ErrInvalidUsage, err)
	}
	if err := requireEOF(decoder); err != nil {
		return Notification{}, fmt.Errorf("%w: decode: %v", ErrInvalidUsage, err)
	}
	if wire.ThreadID == nil || wire.TurnID == nil || wire.TokenUsage == nil || wire.TokenUsage.Last == nil || wire.TokenUsage.Total == nil {
		return Notification{}, fmt.Errorf("%w: missing required notification field", ErrInvalidUsage)
	}
	last, err := decodeRequiredUsage("last", wire.TokenUsage.Last)
	if err != nil {
		return Notification{}, err
	}
	total, err := decodeRequiredUsage("total", wire.TokenUsage.Total)
	if err != nil {
		return Notification{}, err
	}
	return Notification{
		ThreadID: *wire.ThreadID,
		TurnID:   *wire.TurnID,
		TokenUsage: ThreadTokenUsage{
			Last:               last,
			Total:              total,
			ModelContextWindow: wire.TokenUsage.ModelContextWindow,
		},
	}, nil
}

func decodeRequiredUsage(label string, wire *notificationUsageWire) (TokenUsage, error) {
	if wire.InputTokens == nil || wire.CachedInputTokens == nil || wire.OutputTokens == nil || wire.ReasoningOutputTokens == nil || wire.TotalTokens == nil {
		return TokenUsage{}, fmt.Errorf("%w: %s missing required token field", ErrInvalidUsage, label)
	}
	usage := TokenUsage{
		InputTokens:           *wire.InputTokens,
		CachedInputTokens:     *wire.CachedInputTokens,
		OutputTokens:          *wire.OutputTokens,
		ReasoningOutputTokens: *wire.ReasoningOutputTokens,
		TotalTokens:           *wire.TotalTokens,
	}
	if wire.CacheWriteInputTokens != nil {
		value := *wire.CacheWriteInputTokens
		usage.CacheWriteInputTokens = &value
	}
	return usage, nil
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

// BudgetStatus reports a post-observation threshold. It does not claim that
// app-server was hard-capped at Limit.
type BudgetStatus struct {
	Limit     int64 `json:"limit"`
	Used      int64 `json:"used"`
	Exhausted bool  `json:"exhausted"`
	Overshoot int64 `json:"overshoot"`
}

// Receipt is the normalized high-water result for one bound turn. Before is the
// cumulative baseline captured before the turn, After is the retained high-water,
// and Delta is After-Before. Anomalies are sticky and deterministically ordered.
type Receipt struct {
	ThreadID  string        `json:"thread_id"`
	TurnID    string        `json:"turn_id"`
	Source    string        `json:"source"`
	Coverage  string        `json:"coverage"`
	Before    TokenUsage    `json:"before"`
	After     TokenUsage    `json:"after"`
	Delta     TokenUsage    `json:"delta"`
	Anomalies []string      `json:"anomalies"`
	Budget    *BudgetStatus `json:"budget,omitempty"`
}

// Tracker retains a componentwise cumulative high-water for one exact turn.
type Tracker struct {
	threadID           string
	turnID             string
	before             TokenUsage
	after              TokenUsage
	limit              *int64
	observed           bool
	cacheWriteObserved bool
	anomaly            map[string]bool
}

// NewTracker creates a tracker from an explicit cumulative pre-turn baseline.
func NewTracker(threadID, turnID string, baseline TokenUsage, budgetLimit *int64) (*Tracker, error) {
	if strings.TrimSpace(threadID) == "" || strings.TrimSpace(turnID) == "" {
		return nil, fmt.Errorf("%w: empty thread or turn identity", ErrInvalidUsage)
	}
	if err := validateUsage(baseline); err != nil {
		return nil, fmt.Errorf("%w: baseline: %v", ErrInvalidUsage, err)
	}
	var limit *int64
	if budgetLimit != nil {
		if *budgetLimit < 1 {
			return nil, fmt.Errorf("%w: budget limit must be positive", ErrInvalidUsage)
		}
		value := *budgetLimit
		limit = &value
	}
	baseline = cloneUsage(baseline)
	return &Tracker{
		threadID: threadID,
		turnID:   turnID,
		before:   baseline,
		after:    cloneUsage(baseline),
		limit:    limit,
		anomaly:  make(map[string]bool),
	}, nil
}

// FreshTracker creates a tracker whose explicit pre-turn cumulative baseline is zero.
func FreshTracker(threadID, turnID string, budgetLimit *int64) (*Tracker, error) {
	return NewTracker(threadID, turnID, TokenUsage{}, budgetLimit)
}

// Receipt returns the current normalized state. Coverage is UNKNOWN until a clean
// bound observation arrives, and remains UNKNOWN after any cumulative regression.
func (t *Tracker) Receipt() Receipt {
	if t == nil {
		return Receipt{Source: SourceCodexAppServer, Coverage: CoverageUnknown}
	}
	coverage := CoverageUnknown
	if t.observed && len(t.anomaly) == 0 {
		coverage = CoverageObserved
	}
	after := cloneUsage(t.after)
	// A baseline or older snapshot cannot prove an omitted optional counter.
	if !t.cacheWriteObserved {
		after.CacheWriteInputTokens = nil
	}
	result := Receipt{
		ThreadID:  t.threadID,
		TurnID:    t.turnID,
		Source:    SourceCodexAppServer,
		Coverage:  coverage,
		Before:    cloneUsage(t.before),
		After:     after,
		Delta:     subtract(after, t.before),
		Anomalies: anomalyNames(t.anomaly),
	}
	if t.limit != nil {
		used := result.Delta.TotalTokens
		overshoot := int64(0)
		if used > *t.limit {
			overshoot = used - *t.limit
		}
		result.Budget = &BudgetStatus{
			Limit:     *t.limit,
			Used:      used,
			Exhausted: used >= *t.limit,
			Overshoot: overshoot,
		}
	}
	return result
}

// Observe consumes one already-persisted app-server notification. A mismatched
// identity or invalid count leaves the tracker unchanged. A regression retains
// componentwise high-water state, records a sticky anomaly, and returns both the
// resulting receipt and ErrUsageRegression.
func (t *Tracker) Observe(notification Notification) (Receipt, error) {
	if t == nil || t.anomaly == nil || t.threadID == "" || t.turnID == "" {
		return Receipt{Source: SourceCodexAppServer, Coverage: CoverageUnknown}, ErrUninitialized
	}
	if notification.ThreadID != t.threadID || notification.TurnID != t.turnID {
		return t.Receipt(), fmt.Errorf("%w: got thread %q turn %q", ErrIdentityMismatch, notification.ThreadID, notification.TurnID)
	}
	if err := validateUsage(notification.TokenUsage.Last); err != nil {
		return t.Receipt(), fmt.Errorf("%w: last: %v", ErrInvalidUsage, err)
	}
	if err := validateUsage(notification.TokenUsage.Total); err != nil {
		return t.Receipt(), fmt.Errorf("%w: total: %v", ErrInvalidUsage, err)
	}

	t.cacheWriteObserved = notification.TokenUsage.Total.CacheWriteInputTokens != nil
	mergeHighWater(&t.after, notification.TokenUsage.Total, t.anomaly)
	t.observed = true
	receipt := t.Receipt()
	if len(receipt.Anomalies) != 0 {
		return receipt, fmt.Errorf("%w: %s", ErrUsageRegression, strings.Join(receipt.Anomalies, ","))
	}
	return receipt, nil
}

func validateUsage(usage TokenUsage) error {
	values := []struct {
		name  string
		value int64
	}{
		{"inputTokens", usage.InputTokens},
		{"cachedInputTokens", usage.CachedInputTokens},
		{"outputTokens", usage.OutputTokens},
		{"reasoningOutputTokens", usage.ReasoningOutputTokens},
		{"totalTokens", usage.TotalTokens},
	}
	if usage.CacheWriteInputTokens != nil {
		values = append(values, struct {
			name  string
			value int64
		}{"cacheWriteInputTokens", *usage.CacheWriteInputTokens})
	}
	for _, item := range values {
		if item.value < 0 {
			return fmt.Errorf("%s is negative", item.name)
		}
	}
	if usage.CachedInputTokens > usage.InputTokens {
		return errors.New("cachedInputTokens exceeds inputTokens")
	}
	if usage.ReasoningOutputTokens > usage.OutputTokens {
		return errors.New("reasoningOutputTokens exceeds outputTokens")
	}
	if usage.CacheWriteInputTokens != nil && *usage.CacheWriteInputTokens > usage.InputTokens {
		return errors.New("cacheWriteInputTokens exceeds inputTokens")
	}
	if usage.InputTokens > int64(^uint64(0)>>1)-usage.OutputTokens || usage.TotalTokens != usage.InputTokens+usage.OutputTokens {
		return errors.New("totalTokens does not equal inputTokens plus outputTokens")
	}
	return nil
}

func mergeHighWater(after *TokenUsage, current TokenUsage, anomalies map[string]bool) {
	merge := func(name string, high *int64, value int64) {
		if value < *high {
			anomalies[name] = true
			return
		}
		*high = value
	}
	merge("inputTokens", &after.InputTokens, current.InputTokens)
	merge("cachedInputTokens", &after.CachedInputTokens, current.CachedInputTokens)
	merge("outputTokens", &after.OutputTokens, current.OutputTokens)
	merge("reasoningOutputTokens", &after.ReasoningOutputTokens, current.ReasoningOutputTokens)
	merge("totalTokens", &after.TotalTokens, current.TotalTokens)
	if current.CacheWriteInputTokens != nil {
		if after.CacheWriteInputTokens == nil {
			value := *current.CacheWriteInputTokens
			after.CacheWriteInputTokens = &value
		} else {
			merge("cacheWriteInputTokens", after.CacheWriteInputTokens, *current.CacheWriteInputTokens)
		}
	}
}

func subtract(after, before TokenUsage) TokenUsage {
	result := TokenUsage{
		InputTokens:           after.InputTokens - before.InputTokens,
		CachedInputTokens:     after.CachedInputTokens - before.CachedInputTokens,
		OutputTokens:          after.OutputTokens - before.OutputTokens,
		ReasoningOutputTokens: after.ReasoningOutputTokens - before.ReasoningOutputTokens,
		TotalTokens:           after.TotalTokens - before.TotalTokens,
	}
	if after.CacheWriteInputTokens != nil && before.CacheWriteInputTokens != nil {
		value := *after.CacheWriteInputTokens - *before.CacheWriteInputTokens
		result.CacheWriteInputTokens = &value
	}
	return result
}

func cloneUsage(usage TokenUsage) TokenUsage {
	if usage.CacheWriteInputTokens != nil {
		value := *usage.CacheWriteInputTokens
		usage.CacheWriteInputTokens = &value
	}
	return usage
}

func anomalyNames(anomalies map[string]bool) []string {
	order := []string{"inputTokens", "cachedInputTokens", "outputTokens", "reasoningOutputTokens", "totalTokens", "cacheWriteInputTokens"}
	result := make([]string, 0, len(anomalies))
	for _, name := range order {
		if anomalies[name] {
			result = append(result, name)
		}
	}
	return result
}
