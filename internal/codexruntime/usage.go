package codexruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexrpc"
	"github.com/iancuuandrei/fabric/internal/codexusage"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

// UsagePolicy is fixed before turn dispatch. Zero baseline is only legitimate
// here because Execute always creates a new thread, never resumes one.
type UsagePolicy struct {
	Version         int                   `json:"version,omitempty"`
	UnlimitedTokens bool                  `json:"unlimited_tokens,omitempty"`
	ThreadID        string                `json:"thread_id"`
	Baseline        codexusage.TokenUsage `json:"baseline"`
	Budget          int64                 `json:"budget"`
	Required        bool                  `json:"required"`
	Qualified       bool                  `json:"qualified"`
	Origin          string                `json:"origin"`
}

// UsageStart binds a usage tracker to the dispatched turn.
type UsageStart struct {
	TurnID string `json:"turn_id"`
}

// UsageNormalized is the normalized receipt and failure for one notification.
type UsageNormalized struct {
	Version int                `json:"version,omitempty"`
	Receipt codexusage.Receipt `json:"receipt"`
	Failure string             `json:"failure"`
}

// UsageStop records the failure and turn binding for a usage stop.
type UsageStop struct {
	Reason   string `json:"reason"`
	ThreadID string `json:"thread_id"`
	TurnID   string `json:"turn_id"`
}

// Error returns the usage stop reason.
func (s *UsageStop) Error() string { return s.Reason }

// InterruptTarget returns the thread and turn bound to the usage stop.
func (s *UsageStop) InterruptTarget() (string, string) { return s.ThreadID, s.TurnID }

func (a *Adapter) beginUsage(thread codexrpc.ThreadSettings) error {
	zero := int64(0)
	return appendEvent(a.JournalPath, "runtime.usage-baseline", UsagePolicy{Version: 1, UnlimitedTokens: a.UnlimitedTokens, Baseline: codexusage.TokenUsage{CacheWriteInputTokens: &zero}, ThreadID: thread.ThreadID, Budget: a.UsageBudget, Required: a.requiresUsageAdmission(), Qualified: a.UsageQualified, Origin: "FRESH_THREAD"})
}

func (a *Adapter) requiresUsageAdmission() bool {
	return a.RequireLiveUsage || a.UsageBudget > 0
}

func usageRequired(policy *UsagePolicy) bool {
	return policy != nil && policy.Required
}

func normalizeUsage(s *State) UsageNormalized {
	return normalizeUsageWithDecoder(s, 2, codexusage.DecodeNotification)
}

// normalizeUsageLegacy retains the exact strict decoder used by versionless
// historical normalization records. Recorded failures remain authoritative on
// replay even when a newer decoder recognizes an additional native observation.
func normalizeUsageLegacy(s *State) UsageNormalized {
	return normalizeUsageWithDecoder(s, 0, codexusage.DecodeNotificationLegacy)
}

func normalizeUsageWithDecoder(s *State, version int, decode func([]byte) (codexusage.Notification, error)) UsageNormalized {
	n := UsageNormalized{Version: version}
	event, err := decode(s.UsagePending.Params)
	if err != nil {
		n.Failure = "USAGE_INVALID_EVENT"
		if s.usageTracker != nil {
			n.Receipt = s.usageTracker.Receipt()
		} else if version != 0 {
			n.Receipt = unknownUsageReceipt(s)
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
			if version != 0 {
				n.Receipt = unknownUsageReceipt(s)
			}
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

func unknownUsageReceipt(s *State) codexusage.Receipt {
	threadID := ""
	if s != nil && s.UsagePolicy != nil {
		threadID = s.UsagePolicy.ThreadID
	}
	turnID := ""
	if s != nil {
		turnID = s.UsageTurnID
		if turnID == "" {
			turnID = s.TurnID
		}
	}
	return codexusage.Receipt{
		ThreadID:  threadID,
		TurnID:    turnID,
		Source:    codexusage.SourceCodexAppServer,
		Coverage:  codexusage.CoverageUnknown,
		Anomalies: []string{},
	}
}

// observeUsage executes on the serial RPC reader before another tool reply.
// The raw params are durable before normalization or budget mutation.
func (a *Adapter) observeUsage(m codexrpc.Message) error {
	if m.Method == "item/started" || m.Method == "item/completed" {
		return a.observeCompaction(m)
	}
	if m.Method != "thread/tokenUsage/updated" {
		return nil
	}
	before, err := Inspect(a.JournalPath)
	if err != nil {
		return err
	}
	if err := appendEvent(a.JournalPath, "runtime.usage-raw", m); err != nil {
		if !usageRequired(before.UsagePolicy) {
			committed, fallback, inspectErr := inspectOptionalUsageRawFailure(a.JournalPath, before, m, err)
			if inspectErr != nil {
				return inspectErr
			}
			if fallback {
				a.usageMetadataUnavailable = true
				return nil
			}
			if !committed {
				return err
			}
		} else {
			return err
		}
	}
	s, err := Inspect(a.JournalPath)
	if err != nil {
		return err
	}
	n := normalizeUsage(&s)
	if err := appendEvent(a.JournalPath, "runtime.usage-normalized", n); err != nil {
		if !usageRequired(s.UsagePolicy) {
			committed, fallback, inspectErr := inspectOptionalUsageNormalizationFailure(a.JournalPath, s, n, err)
			if inspectErr != nil {
				return inspectErr
			}
			if fallback {
				a.usageMetadataUnavailable = true
				return nil
			}
			if !committed {
				return err
			}
		}
		if usageRequired(s.UsagePolicy) {
			return err
		}
	}
	if n.Failure != "" {
		if !usageRequired(s.UsagePolicy) && n.Failure != "BUDGET_EXHAUSTED" {
			return nil
		}
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

// inspectOptionalUsageRawFailure permits optional degradation only when the
// readable journal still proves the same invocation and the raw notification
// was not appended. A lock failure or changed journal identity stops capture.
func inspectOptionalUsageRawFailure(path string, before State, message codexrpc.Message, appendErr error) (committed, fallback bool, err error) {
	if errors.Is(appendErr, journal.ErrLockUnavailable) {
		return false, false, appendErr
	}
	current, inspectErr := Inspect(path)
	if inspectErr != nil {
		return false, false, errors.Join(appendErr, inspectErr)
	}
	if err := sameUsageJournalIdentity(before, current); err != nil {
		return false, false, errors.Join(appendErr, err)
	}
	if before.UsagePending != nil || before.UsageFailure != "" || before.Result != nil || before.SemanticResult != nil {
		return false, false, errors.Join(appendErr, errors.New("usage raw append did not start from an empty pending state"))
	}
	if current.UsagePending != nil {
		if current.UsagePending.Method != message.Method || !bytes.Equal(current.UsagePending.Params, message.Params) || current.UsageFailure != "" {
			return false, false, errors.Join(appendErr, errors.New("usage raw append outcome is not attributable"))
		}
		return true, false, nil
	}
	if current.UsageFailure != before.UsageFailure || current.TurnStatus != before.TurnStatus || current.TurnPending != before.TurnPending {
		return false, false, errors.Join(appendErr, errors.New("usage journal changed during raw notification append"))
	}
	return false, true, nil
}

func sameUsageJournalIdentity(before, current State) error {
	if before.Intent == nil || current.Intent == nil || before.Intent.Invocation.ID != current.Intent.Invocation.ID || before.Intent.Directory != current.Intent.Directory ||
		before.Thread == nil || current.Thread == nil || before.Thread.ThreadID != current.Thread.ThreadID ||
		before.TurnID != current.TurnID || before.UsageTurnID != current.UsageTurnID || before.UsagePolicy == nil || current.UsagePolicy == nil {
		return errors.New("usage journal identity changed")
	}
	beforePolicy, beforeErr := canonical.Hash("harness.codex-usage-policy.v1", *before.UsagePolicy)
	currentPolicy, currentErr := canonical.Hash("harness.codex-usage-policy.v1", *current.UsagePolicy)
	if beforeErr != nil || currentErr != nil || beforePolicy != currentPolicy {
		return errors.Join(beforeErr, currentErr, errors.New("usage policy changed"))
	}
	return nil
}

// inspectOptionalUsageNormalizationFailure permits optional degradation only
// when the readable journal still proves the same invocation, thread, turn,
// usage policy, and raw notification. A lock failure remains an observer stop;
// it is not evidence that the normalizer append was safely skipped.
func inspectOptionalUsageNormalizationFailure(path string, before State, normalized UsageNormalized, appendErr error) (committed, fallback bool, err error) {
	current, inspectErr := Inspect(path)
	if inspectErr != nil {
		return false, false, errors.Join(appendErr, inspectErr)
	}
	if identityErr := sameUsageJournalIdentity(before, current); identityErr != nil {
		return false, false, errors.Join(appendErr, identityErr)
	}
	if before.UsagePending == nil {
		return false, false, errors.Join(appendErr, errors.New("usage normalization lacks its pending raw notification"))
	}
	if current.UsagePending == nil {
		wantReceipt := &normalized.Receipt
		if current.UsagePolicy.Version == 1 && normalized.Failure != "" && normalized.Failure != "BUDGET_EXHAUSTED" {
			wantReceipt = nil
		}
		if current.UsageFailure != normalized.Failure || !sameUsageReceipt(current.UsageReceipt, wantReceipt) {
			return false, false, errors.Join(appendErr, errors.New("usage normalization append outcome is not attributable"))
		}
		return true, false, nil
	}
	if current.UsagePending.Method != before.UsagePending.Method || !bytes.Equal(current.UsagePending.Params, before.UsagePending.Params) || current.UsageFailure != before.UsageFailure {
		return false, false, errors.Join(appendErr, errors.New("usage raw notification changed during normalization"))
	}
	if errors.Is(appendErr, journal.ErrLockUnavailable) {
		return false, false, appendErr
	}
	return false, true, nil
}

func sameUsageReceipt(a, b *codexusage.Receipt) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	left, leftErr := canonical.Hash("harness.codex-usage-receipt.v1", *a)
	right, rightErr := canonical.Hash("harness.codex-usage-receipt.v1", *b)
	return leftErr == nil && rightErr == nil && left == right
}

func (s *State) usageEvent(kind string, raw json.RawMessage) error {
	switch kind {
	case "runtime.usage-baseline":
		var p UsagePolicy
		if s.Thread == nil || s.TurnPending || s.UsagePolicy != nil || canonical.Decode(raw, &p) != nil || p.Version < 0 || p.Version > 1 || p.ThreadID != s.Thread.ThreadID || p.Origin != "FRESH_THREAD" || p.Budget < 0 || p.UnlimitedTokens && p.Budget != 0 || p.Required && (!p.Qualified || p.Budget < 1 && !p.UnlimitedTokens) || p.Version == 1 && p.Budget > 0 && !p.Required {
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
		if s.UsagePolicy.Version != 1 || s.UsageFailure == "" && s.UsagePending == nil {
			receipt := t.Receipt()
			s.UsageReceipt = &receipt
		}
	case "runtime.usage-raw":
		var m codexrpc.Message
		if s.UsagePolicy == nil || s.UsagePending != nil || s.UsageFailure != "" || s.SemanticResult != nil || s.Result != nil || canonical.Decode(raw, &m) != nil || m.Method != "thread/tokenUsage/updated" || len(m.Params) > 16384 {
			return errors.New("invalid usage raw transition")
		}
		// Until the newest notification is validated, earlier totals are not a
		// final accounting observation for this turn.
		if s.UsagePolicy.Version == 1 {
			s.UsageReceipt = nil
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
		var want UsageNormalized
		switch got.Version {
		case 0:
			want = normalizeUsageLegacy(s)
		case 2:
			want = normalizeUsage(s)
		default:
			return errors.New("unsupported usage normalization version")
		}
		a, _ := canonical.Hash("usage", got)
		b, _ := canonical.Hash("usage", want)
		if a != b {
			return errors.New("usage normalization differs from raw evidence")
		}
		s.UsageReceipt = &got.Receipt
		if s.UsagePolicy.Version == 1 && got.Failure != "" && got.Failure != "BUDGET_EXHAUSTED" {
			s.UsageReceipt = nil
		}
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

// ErrUsageReconciliation blocks admission while required accounting is unresolved.
var ErrUsageReconciliation = errors.New("usage requires reconciliation")

func (a *Adapter) attachUsage(result *runtime.Result) error {
	s, err := Inspect(a.JournalPath)
	if err != nil {
		return err
	}
	if s.UsageFailure != "" || s.UsagePending != nil {
		return ErrUsageReconciliation
	}
	if s.UsagePolicy == nil {
		return nil
	} // legacy recovery retains old evidence
	if s.UsagePolicy.Required && s.RouteResumes > 0 && !s.UsageHydrated {
		err := appendEvent(a.JournalPath, "runtime.usage-blocked", UsageStop{"USAGE_HYDRATION_MISSING", s.Thread.ThreadID, s.TurnID})
		return errors.Join(ErrUsageReconciliation, errors.New("USAGE_HYDRATION_MISSING"), err)
	}
	if s.UsageReceipt == nil || s.UsageReceipt.Coverage != "OBSERVED" {
		if s.UsagePolicy.Required {
			err := appendEvent(a.JournalPath, "runtime.usage-blocked", UsageStop{"USAGE_MISSING", s.Thread.ThreadID, s.TurnID})
			return errors.Join(ErrUsageReconciliation, errors.New("USAGE_MISSING"), err)
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
