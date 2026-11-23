package cost

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// PricePerMillion declares USD cost per 1,000,000 tokens for a model.
type PricePerMillion struct {
	InputUSD       float64 `toml:"input_usd" json:"input_usd"`
	CachedInputUSD float64 `toml:"cached_input_usd" json:"cached_input_usd"`
	CacheWriteUSD  float64 `toml:"cache_write_usd" json:"cache_write_usd"`
	OutputUSD      float64 `toml:"output_usd" json:"output_usd"`
}

// Usage tracks granular token categories for one turn or an entire run.
type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens,omitempty"`
}

// Add accumulates another Usage snapshot into u.
func (u *Usage) Add(other Usage) {
	u.InputTokens += other.InputTokens
	u.CachedTokens += other.CachedTokens
	u.CacheWriteTokens += other.CacheWriteTokens
	u.OutputTokens += other.OutputTokens
	u.ReasoningTokens += other.ReasoningTokens
}

// TotalTokens returns total input + output tokens.
func (u Usage) TotalTokens() int64 {
	return u.InputTokens + u.OutputTokens
}

// Estimate captures computed USD spend and whether pricing was known.
type Estimate struct {
	Model         string  `json:"model"`
	KnownPricing  bool    `json:"known_pricing"`
	EstimatedUSD  float64 `json:"estimated_usd"`
	CacheSavedUSD float64 `json:"cache_saved_usd,omitempty"`
}

// Budget declares hard per-run resource circuit breakers in fabric.toml.
type Budget struct {
	MaxUSD         float64                    `toml:"max_usd" json:"max_usd,omitempty"`
	MaxTokens      int64                      `toml:"max_tokens" json:"max_tokens,omitempty"`
	MaxTurns       int                        `toml:"max_turns" json:"max_turns,omitempty"`
	MaxWallSeconds int                        `toml:"max_wall_seconds" json:"max_wall_seconds,omitempty"`
	CustomPricing  map[string]PricePerMillion `toml:"pricing" json:"pricing,omitempty"`
}

// Validate checks that configured budget limits are non-negative and finite.
func (b Budget) Validate() error {
	if b.MaxUSD < 0 || b.MaxUSD > 10000 {
		return errors.New("budget max_usd must be between 0 and 10000")
	}
	if b.MaxTokens < 0 || b.MaxTokens > 1_000_000_000 {
		return errors.New("budget max_tokens out of bounds")
	}
	if b.MaxTurns < 0 || b.MaxTurns > 500 {
		return errors.New("budget max_turns must be between 0 and 500")
	}
	if b.MaxWallSeconds < 0 || b.MaxWallSeconds > 86400 {
		return errors.New("budget max_wall_seconds must be between 0 and 86400")
	}
	return nil
}

// ErrBudgetExceeded is returned when a hard resource ceiling is reached.
var ErrBudgetExceeded = errors.New("run budget exceeded")

// DefaultPricing maps model family prefixes to conservative USD-per-1M rates.
var DefaultPricing = []struct {
	Prefix string
	Price  PricePerMillion
}{
	{"claude-opus-4", PricePerMillion{InputUSD: 15.0, CachedInputUSD: 1.5, CacheWriteUSD: 18.75, OutputUSD: 75.0}},
	{"claude-sonnet-4", PricePerMillion{InputUSD: 3.0, CachedInputUSD: 0.3, CacheWriteUSD: 3.75, OutputUSD: 15.0}},
	{"claude-haiku-4", PricePerMillion{InputUSD: 0.8, CachedInputUSD: 0.08, CacheWriteUSD: 1.0, OutputUSD: 4.0}},
	{"claude-3-7-sonnet", PricePerMillion{InputUSD: 3.0, CachedInputUSD: 0.3, CacheWriteUSD: 3.75, OutputUSD: 15.0}},
	{"gpt-5-mini", PricePerMillion{InputUSD: 0.25, CachedInputUSD: 0.025, CacheWriteUSD: 0.25, OutputUSD: 2.0}},
	{"gpt-5", PricePerMillion{InputUSD: 2.5, CachedInputUSD: 0.25, CacheWriteUSD: 2.5, OutputUSD: 10.0}},
	{"gpt-4.1-mini", PricePerMillion{InputUSD: 0.4, CachedInputUSD: 0.1, CacheWriteUSD: 0.4, OutputUSD: 1.6}},
	{"gpt-4.1", PricePerMillion{InputUSD: 2.0, CachedInputUSD: 0.5, CacheWriteUSD: 2.0, OutputUSD: 8.0}},
	{"o3", PricePerMillion{InputUSD: 2.0, CachedInputUSD: 0.5, CacheWriteUSD: 2.0, OutputUSD: 8.0}},
	{"o4-mini", PricePerMillion{InputUSD: 1.1, CachedInputUSD: 0.275, CacheWriteUSD: 1.1, OutputUSD: 4.4}},
	{"gemini-2.5-pro", PricePerMillion{InputUSD: 1.25, CachedInputUSD: 0.31, CacheWriteUSD: 1.25, OutputUSD: 10.0}},
	{"gemini-2.5-flash", PricePerMillion{InputUSD: 0.15, CachedInputUSD: 0.0375, CacheWriteUSD: 0.15, OutputUSD: 0.6}},
	{"deepseek-r1", PricePerMillion{InputUSD: 0.55, CachedInputUSD: 0.14, CacheWriteUSD: 0.55, OutputUSD: 2.19}},
	{"deepseek-chat", PricePerMillion{InputUSD: 0.27, CachedInputUSD: 0.07, CacheWriteUSD: 0.27, OutputUSD: 1.10}},
	{"ollama/", PricePerMillion{InputUSD: 0, CachedInputUSD: 0, CacheWriteUSD: 0, OutputUSD: 0}},
	{"local/", PricePerMillion{InputUSD: 0, CachedInputUSD: 0, CacheWriteUSD: 0, OutputUSD: 0}},
}

// LookupPrice resolves model pricing from custom overrides first, then defaults.
func LookupPrice(model string, custom map[string]PricePerMillion) (PricePerMillion, bool) {
	lower := strings.ToLower(strings.TrimSpace(model))
	for k, v := range custom {
		if strings.ToLower(strings.TrimSpace(k)) == lower {
			return v, true
		}
	}
	for _, entry := range DefaultPricing {
		if strings.HasPrefix(lower, entry.Prefix) {
			return entry.Price, true
		}
	}
	return PricePerMillion{}, false
}

// EstimateUSD computes USD cost and prompt-cache savings for a model and usage.
func EstimateUSD(model string, u Usage, custom map[string]PricePerMillion) Estimate {
	p, ok := LookupPrice(model, custom)
	if !ok {
		return Estimate{Model: model, KnownPricing: false}
	}
	uncached := u.InputTokens - u.CachedTokens - u.CacheWriteTokens
	if uncached < 0 {
		uncached = 0
	}
	usd := (float64(uncached)*p.InputUSD +
		float64(u.CachedTokens)*p.CachedInputUSD +
		float64(u.CacheWriteTokens)*p.CacheWriteUSD +
		float64(u.OutputTokens)*p.OutputUSD) / 1_000_000.0
	saved := float64(u.CachedTokens) * (p.InputUSD - p.CachedInputUSD) / 1_000_000.0
	if saved < 0 {
		saved = 0
	}
	return Estimate{
		Model:         model,
		KnownPricing:  true,
		EstimatedUSD:  usd,
		CacheSavedUSD: saved,
	}
}

// Tracker enforces hard budget circuit breakers across turns of a run.
type Tracker struct {
	Budget    Budget
	Model     string
	StartedAt time.Time
	Turns     int
	Usage     Usage
}

// NewTracker initializes a run budget tracker.
func NewTracker(model string, b Budget) *Tracker {
	return &Tracker{Budget: b, Model: model, StartedAt: time.Now().UTC()}
}

// RecordTurn records one completed model turn's token usage.
func (t *Tracker) RecordTurn(u Usage) {
	t.Turns++
	t.Usage.Add(u)
}

// CurrentEstimate returns the cumulative USD estimate so far.
func (t *Tracker) CurrentEstimate() Estimate {
	return EstimateUSD(t.Model, t.Usage, t.Budget.CustomPricing)
}

// CheckBeforeTurn returns ErrBudgetExceeded if any hard ceiling has been hit.
func (t *Tracker) CheckBeforeTurn() error {
	if t.Budget.MaxTurns > 0 && t.Turns >= t.Budget.MaxTurns {
		return fmt.Errorf("%w: max_turns (%d) reached", ErrBudgetExceeded, t.Budget.MaxTurns)
	}
	if t.Budget.MaxTokens > 0 && t.Usage.TotalTokens() >= t.Budget.MaxTokens {
		return fmt.Errorf("%w: max_tokens (%d >= %d) reached", ErrBudgetExceeded, t.Usage.TotalTokens(), t.Budget.MaxTokens)
	}
	if t.Budget.MaxWallSeconds > 0 && time.Since(t.StartedAt) >= time.Duration(t.Budget.MaxWallSeconds)*time.Second {
		return fmt.Errorf("%w: max_wall_seconds (%ds) reached", ErrBudgetExceeded, t.Budget.MaxWallSeconds)
	}
	if t.Budget.MaxUSD > 0 {
		est := t.CurrentEstimate()
		if est.KnownPricing && est.EstimatedUSD >= t.Budget.MaxUSD {
			return fmt.Errorf("%w: max_usd ($%.4f >= $%.2f) reached", ErrBudgetExceeded, est.EstimatedUSD, t.Budget.MaxUSD)
		}
	}
	return nil
}
