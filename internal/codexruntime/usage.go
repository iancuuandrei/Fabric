package codexruntime

import (
	"encoding/json"
	"errors"
	"fmt"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexrpc"
	"harness.local/engorch/internal/codexusage"
	"harness.local/engorch/internal/runtime"
)

// UsagePolicy is fixed before turn dispatch. Zero baseline is only legitimate
// here because Execute always creates a new thread, never resumes one.
type UsagePolicy struct {
	UnlimitedTokens bool                  `json:"unlimited_tokens,omitempty"`
	ThreadID        string                `json:"thread_id"`
	Baseline        codexusage.TokenUsage `json:"baseline"`
	Budget          int64                 `json:"budget"`
	Required        bool                  `json:"required"`
	Qualified       bool                  `json:"qualified"`
	Origin          string                `json:"origin"`
}
type UsageStart struct {
	TurnID string `json:"turn_id"`
}
type UsageNormalized struct {
	Receipt codexusage.Receipt `json:"receipt"`
	Failure string             `json:"failure"`
}
type UsageStop struct {
	Reason   string `json:"reason"`
	ThreadID string `json:"thread_id"`
	TurnID   string `json:"turn_id"`
}

func (s *UsageStop) Error() string                     { return s.Reason }
func (s *UsageStop) InterruptTarget() (string, string) { return s.ThreadID, s.TurnID }

func (a *Adapter) beginUsage(thread codexrpc.ThreadSettings) error {
	zero := int64(0)
	return appendEvent(a.JournalPath, "runtime.usage-baseline", UsagePolicy{UnlimitedTokens: a.UnlimitedTokens, Baseline: codexusage.TokenUsage{CacheWriteInputTokens: &zero}, ThreadID: thread.ThreadID, Budget: a.UsageBudget, Required: a.RequireLiveUsage, Qualified: a.UsageQualified, Origin: "FRESH_THREAD"})
}

func normalizeUsage(s *State) UsageNormalized {
	n := UsageNormalized{}
	event, err := codexusage.DecodeNotification(s.UsagePending.Params)
	if err != nil {
		n.Failure = "USAGE_INVALID_EVENT"
		if s.usageTracker != nil {
			n.Receipt = s.usageTracker.Receipt()
		}
		return n
	}
	if s.usageTracker == nil {
		// The notification can precede the turn/start response. Bind provisionally
		// only inside this fresh thread's single outstanding turn intent. The actual
		// response must later match; no second turn is created by this adapter.
		if !s.TurnPending || event.ThreadID != s.Thread.ThreadID || event.TurnID == "" || s.ToolTurnID != "" && s.ToolTurnID != event.TurnID {
			n.Failure = "USAGE_TURN_IDENTITY_UNKNOWN"
			return n
		}
		var budget *int64
		if s.UsagePolicy.Budget > 0 {
			budget = &s.UsagePolicy.Budget
		}
		s.usageTracker, err = codexusage.NewTracker(s.Thread.ThreadID, event.TurnID, s.UsagePolicy.Baseline, budget)
		if err != nil {
			n.Failure = err.Error()
			return n
		}
		s.UsageTurnID = event.TurnID
	}
	n.Receipt, err = s.usageTracker.Observe(event)
	if err != nil {
		n.Failure = err.Error()
	}
	if n.Failure == "" && n.Receipt.Budget != nil && n.Receipt.Budget.Exhausted {
		n.Failure = "BUDGET_EXHAUSTED"
	}
	return n
}

// observeUsage executes on the serial RPC reader before another tool reply.
// The raw params are durable before normalization or budget mutation.
func (a *Adapter) observeUsage(m codexrpc.Message) error {
	if m.Method != "thread/tokenUsage/updated" {
		return nil
	}
	if err := appendEvent(a.JournalPath, "runtime.usage-raw", m); err != nil {
		return err
	}
	s, err := Inspect(a.JournalPath)
	if err != nil {
		return err
	}
	n := normalizeUsage(&s)
	if err := appendEvent(a.JournalPath, "runtime.usage-normalized", n); err != nil {
		return err
	}
	if n.Failure != "" {
		stop := UsageStop{n.Failure, s.Thread.ThreadID, s.UsageTurnID}
		if stop.TurnID == "" {
			return errors.New(n.Failure)
		}
		if err := appendEvent(a.JournalPath, "runtime.usage-interrupt-intent", stop); err != nil {
			return err
		}
		return &stop
	}
	return nil
}

func (s *State) usageEvent(kind string, raw json.RawMessage) error {
	switch kind {
	case "runtime.usage-baseline":
		var p UsagePolicy
		if s.Thread == nil || s.TurnPending || s.UsagePolicy != nil || canonical.Decode(raw, &p) != nil || p.ThreadID != s.Thread.ThreadID || p.Origin != "FRESH_THREAD" || p.Budget < 0 || p.UnlimitedTokens && p.Budget != 0 || p.Required && (!p.Qualified || p.Budget < 1 && !p.UnlimitedTokens) {
			return errors.New("invalid usage baseline")
		}
		zeroCount := int64(0)
		zero := codexusage.TokenUsage{}
		if p.Baseline.CacheWriteInputTokens != nil {
			zero.CacheWriteInputTokens = &zeroCount
		}
		a, _ := canonical.Hash("usage", p.Baseline)
		b, _ := canonical.Hash("usage", zero)
		if a != b {
			return errors.New("fresh thread requires zero baseline")
		}
		s.UsagePolicy = &p
	case "runtime.usage-start":
		var start UsageStart
		if s.UsagePolicy == nil || canonical.Decode(raw, &start) != nil || start.TurnID == "" || start.TurnID != s.TurnID {
			return errors.New("invalid usage turn binding")
		}
		if s.usageTracker != nil {
			if start.TurnID != s.UsageTurnID {
				return errors.New("provisional usage turn differs")
			}
			return nil
		}
		var budget *int64
		if s.UsagePolicy.Budget > 0 {
			budget = &s.UsagePolicy.Budget
		}
		t, err := codexusage.NewTracker(s.Thread.ThreadID, start.TurnID, s.UsagePolicy.Baseline, budget)
		if err != nil {
			return err
		}
		s.usageTracker = t
		s.UsageTurnID = start.TurnID
		receipt := t.Receipt()
		s.UsageReceipt = &receipt
	case "runtime.usage-raw":
		var m codexrpc.Message
		if s.UsagePolicy == nil || s.UsagePending != nil || s.UsageFailure != "" || s.Result != nil || canonical.Decode(raw, &m) != nil || m.Method != "thread/tokenUsage/updated" || len(m.Params) > 16384 {
			return errors.New("invalid usage raw transition")
		}
		s.UsagePending = &m
	case "runtime.usage-normalized":
		if s.UsagePending == nil {
			return errors.New("usage normalization lacks raw evidence")
		}
		var got UsageNormalized
		if err := canonical.Decode(raw, &got); err != nil {
			return err
		}
		want := normalizeUsage(s)
		a, _ := canonical.Hash("usage", got)
		b, _ := canonical.Hash("usage", want)
		if a != b {
			return errors.New("usage normalization differs from raw evidence")
		}
		s.UsageReceipt = &got.Receipt
		s.UsageFailure = got.Failure
		s.UsagePending = nil
		if s.RouteResumes > 0 && got.Failure == "" {
			s.UsageHydrated = true
		}
	case "runtime.usage-blocked":
		var stop UsageStop
		if s.UsagePolicy == nil || !s.UsagePolicy.Required || s.UsageFailure != "" || canonical.Decode(raw, &stop) != nil || (stop.Reason != "USAGE_MISSING" && stop.Reason != "USAGE_HYDRATION_MISSING") || stop.ThreadID != s.Thread.ThreadID || stop.TurnID != s.TurnID {
			return errors.New("invalid missing usage stop")
		}
		if stop.Reason == "USAGE_MISSING" && s.UsageReceipt != nil && s.UsageReceipt.Coverage == "OBSERVED" {
			return errors.New("usage is observed")
		}
		if stop.Reason == "USAGE_HYDRATION_MISSING" && (s.RouteResumes == 0 || s.UsageHydrated) {
			return errors.New("usage hydration stop invalid")
		}
		s.UsageFailure = stop.Reason
	case "runtime.usage-interrupt-intent":
		var stop UsageStop
		if s.UsageInterrupt || canonical.Decode(raw, &stop) != nil || s.UsageFailure == "" || stop.Reason != s.UsageFailure || stop.ThreadID != s.Thread.ThreadID || stop.TurnID != s.UsageTurnID {
			return errors.New("invalid usage interrupt intent")
		}
		s.UsageInterrupt = true
	default:
		return fmt.Errorf("unknown usage event %s", kind)
	}
	return nil
}

func (a *Adapter) attachUsage(result *runtime.Result) error {
	s, err := Inspect(a.JournalPath)
	if err != nil {
		return err
	}
	if s.UsageFailure != "" || s.UsagePending != nil {
		return errors.New("usage admission blocked")
	}
	if s.UsagePolicy == nil {
		return nil
	} // legacy recovery retains old evidence
	if s.UsagePolicy.Required && s.RouteResumes > 0 && !s.UsageHydrated {
		err := appendEvent(a.JournalPath, "runtime.usage-blocked", UsageStop{"USAGE_HYDRATION_MISSING", s.Thread.ThreadID, s.TurnID})
		return errors.Join(errors.New("USAGE_HYDRATION_MISSING"), err)
	}
	if s.UsageReceipt == nil || s.UsageReceipt.Coverage != "OBSERVED" {
		if s.UsagePolicy.Required {
			err := appendEvent(a.JournalPath, "runtime.usage-blocked", UsageStop{"USAGE_MISSING", s.Thread.ThreadID, s.TurnID})
			return errors.Join(errors.New("USAGE_MISSING"), err)
		}
		return nil
	}
	result.Usage.Accounting = s.UsageReceipt
	result.Usage.InputTokens = &s.UsageReceipt.Delta.InputTokens
	result.Usage.OutputTokens = &s.UsageReceipt.Delta.OutputTokens
	return nil
}

func (s *State) validateUsageResult(result runtime.Result) error {
	if s.UsagePolicy == nil {
		if result.Usage.Accounting != nil {
			return errors.New("unbound usage receipt")
		}
		return nil
	}
	if s.UsageReceipt == nil || s.UsageReceipt.Coverage != "OBSERVED" {
		if s.UsagePolicy.Required || result.Usage.Accounting != nil {
			return errors.New("usage evidence required")
		}
		return nil
	}
	a, _ := canonical.Hash("usage", s.UsageReceipt)
	b, _ := canonical.Hash("usage", result.Usage.Accounting)
	if a != b || result.Usage.InputTokens == nil || result.Usage.OutputTokens == nil || *result.Usage.InputTokens != s.UsageReceipt.Delta.InputTokens || *result.Usage.OutputTokens != s.UsageReceipt.Delta.OutputTokens {
		return errors.New("runtime usage receipt differs from journal")
	}
	return nil
}
