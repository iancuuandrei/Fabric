package codexruntime

import (
	"errors"
	"path/filepath"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexrpc"
	"harness.local/engorch/internal/codexusage"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
)

// Intent binds the invocation and workspace before any provider thread creation.
type Intent struct {
	Invocation runtime.Invocation `json:"invocation"`
	Directory  string             `json:"directory"`
}

// State is derived exclusively from validated durable events.
// RouteEvidence separates immutable dispatch configuration from current continuation.
type RouteEvidence struct {
	NotificationCoverage      string                   `json:"notification_coverage"`
	HistoricalRouteStatus     string                   `json:"historical_route_status"`
	DispatchObserved          *codexrpc.ThreadSettings `json:"dispatch_observed"`
	ContinuationConfiguration *codexrpc.ThreadSettings `json:"continuation_configuration"`
	ContinuationStatus        string                   `json:"continuation_status"`
}

type State struct {
	UsageTurnID    string              `json:"usage_turn_id,omitempty"`
	UsageInterrupt bool                `json:"usage_interrupt_intent,omitempty"`
	UsagePolicy    *UsagePolicy        `json:"usage_policy,omitempty"`
	UsagePending   *codexrpc.Message   `json:"usage_pending,omitempty"`
	UsageReceipt   *codexusage.Receipt `json:"usage_receipt,omitempty"`
	UsageHydrated  bool                `json:"usage_hydrated,omitempty"`
	UsageFailure   string              `json:"usage_failure,omitempty"`
	usageTracker   *codexusage.Tracker

	NotificationStreamComplete bool                       `json:"notification_stream_complete,omitempty"`
	TerminalResponse           *TerminalResponseTelemetry `json:"terminal_response,omitempty"`
	RouteEvidence              *RouteEvidence             `json:"route_evidence,omitempty"`
	ExecutionOutcome           string                     `json:"execution_outcome,omitempty"`
	Candidate                  *CandidateBinding          `json:"candidate,omitempty"`
	RI                         *RIBinding                 `json:"ri"`
	Lexical                    *LexicalBinding            `json:"lexical,omitempty"`
	LexicalRecord              *LexicalRecord             `json:"lexical_record,omitempty"`
	Source                     *repository.Identity       `json:"source"`
	ToolTurnID                 string                     `json:"tool_turn_id"`
	PendingTool                *ToolRequest               `json:"pending_tool"`
	ToolResponses              []ToolResponse             `json:"tool_responses"`
	Intent                     *Intent                    `json:"intent"`
	Thread                     *codexrpc.ThreadSettings   `json:"thread"`
	TurnPending                bool                       `json:"turn_pending"`
	TurnID                     string                     `json:"turn_id"`
	TurnStatus                 string                     `json:"turn_status"`
	Result                     *runtime.Result            `json:"result"`
	RouteResumePending         bool                       `json:"route_resume_pending,omitempty"`
	RouteResumes               int                        `json:"route_resumes,omitempty"`
	RouteObserved              *codexrpc.ThreadSettings   `json:"route_observed,omitempty"`
	RouteFailure               string                     `json:"route_failure,omitempty"`
}

// RouteFailureEvidence retains the classification and any actual response.
type RouteFailureEvidence struct {
	Reason   string                   `json:"reason"`
	Observed *codexrpc.ThreadSettings `json:"observed,omitempty"`
	Reroute  *codexrpc.Message        `json:"reroute,omitempty"`
}

// TurnResult binds admitted output to the recorded provider turn.
type TurnResult struct {
	TurnID string         `json:"turn_id"`
	Result runtime.Result `json:"result"`
}

// TurnStatus preserves observed provider lifecycle separately from output admission.
type TurnStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func replay(events []journal.Event) (State, error) {
	s := State{}
	for _, e := range events {
		switch e.Kind {
		case "runtime.usage-baseline", "runtime.usage-start", "runtime.usage-raw", "runtime.usage-normalized", "runtime.usage-blocked", "runtime.usage-interrupt-intent":
			if err := s.usageEvent(e.Kind, e.Payload); err != nil {
				return s, err
			}
		case "runtime.stream-terminal":
			var observed TurnStatus
			if s.TurnID == "" || s.Result != nil || s.NotificationStreamComplete || s.RouteFailure != "" || canonical.Decode(e.Payload, &observed) != nil || observed.ID != s.TurnID || (observed.Status != "completed" && observed.Status != "failed" && observed.Status != "interrupted") {
				return s, errors.New("invalid terminal stream observation")
			}
			s.NotificationStreamComplete = true
		case "runtime.terminal-response":
			var observed TerminalResponseTelemetry
			if s.TurnID == "" || s.Result != nil || s.TerminalResponse != nil || canonical.Decode(e.Payload, &observed) != nil {
				return s, errors.New("invalid terminal response telemetry transition")
			}
			if err := validateTerminalResponseTelemetry(observed, s.Thread.ThreadID, s.TurnID); err != nil {
				return s, err
			}
			// Diagnostic telemetry is intentionally inert. Only turn-status and
			// stream-terminal events establish outcome and notification coverage.
			s.TerminalResponse = &observed
		case "runtime.route-contradiction":
			if s.Intent == nil || s.Result != nil {
				return s, errors.New("route contradiction transition rejected")
			}
			var failure RouteFailureEvidence
			if canonical.Decode(e.Payload, &failure) != nil || failure.Reason != "ROUTE_IDENTITY_CONTRADICTION" {
				return s, errors.New("invalid route contradiction")
			}
			s.RouteFailure = failure.Reason
		case "runtime.route-resume-intent":
			if s.Thread == nil || s.TurnID == "" || s.Result != nil || s.RouteResumePending || s.RouteResumes >= 64 || (s.RouteFailure == "ROUTE_IDENTITY_CONTRADICTION" || s.RouteFailure == "CONTINUATION_ROUTE_MISMATCH") {
				return s, errors.New("route resume transition rejected")
			}
			var thread codexrpc.ThreadSettings
			if err := canonical.Decode(e.Payload, &thread); err != nil {
				return s, err
			}
			want, _ := canonical.Hash("runtime.route-resume", s.Thread)
			got, err := canonical.Hash("runtime.route-resume", thread)
			if err != nil || got != want {
				return s, errors.New("route resume intent changed")
			}
			s.UsageHydrated = false
			s.RouteResumePending = true
			s.RouteObserved = nil
			s.RouteFailure = ""
			s.RouteResumes++
		case "runtime.route-resumed":
			if !s.RouteResumePending || (s.RouteFailure == "ROUTE_IDENTITY_CONTRADICTION" || s.RouteFailure == "CONTINUATION_ROUTE_MISMATCH") {
				return s, errors.New("route observation lacks intent")
			}
			var thread codexrpc.ThreadSettings
			if err := canonical.Decode(e.Payload, &thread); err != nil {
				return s, err
			}
			if thread.ThreadID != s.Thread.ThreadID || thread.Effort == nil || *thread.Effort != s.Intent.Invocation.Profile.Effort {
				return s, errors.New("route resume observation incomplete or changed")
			}
			if err := thread.Validate(s.Intent.Invocation.Profile, s.Intent.Directory); err != nil {
				return s, err
			}
			s.RouteObserved = &thread
			s.RouteResumePending = false
			s.RouteFailure = ""
		case "runtime.route-resume-failed":
			if !s.RouteResumePending || (s.RouteFailure == "ROUTE_IDENTITY_CONTRADICTION" || s.RouteFailure == "CONTINUATION_ROUTE_MISMATCH") {
				return s, errors.New("route failure lacks intent")
			}
			var failure RouteFailureEvidence
			if err := canonical.Decode(e.Payload, &failure); err != nil {
				return s, err
			}
			if failure.Reason != "UNKNOWN" && failure.Reason != "ROUTE_IDENTITY_CONTRADICTION" && failure.Reason != "CONTINUATION_ROUTE_MISMATCH" {
				return s, errors.New("invalid route failure")
			}
			// Legacy R1 classified resume mismatches as contradictions. Interpret their
			// continuation context without rewriting the original event bytes.
			s.RouteFailure = failure.Reason
			if failure.Reason == "ROUTE_IDENTITY_CONTRADICTION" {
				s.RouteFailure = "CONTINUATION_ROUTE_MISMATCH"
			}
			s.RouteObserved = failure.Observed
			s.RouteResumePending = false
		case "runtime.lexical", "runtime.lexical-record", "runtime.candidate", "runtime.ri", "runtime.source", "runtime.tool-request", "runtime.tool-response":
			if err := s.toolEvent(e.Kind, e.Payload); err != nil {
				return s, err
			}
		case "runtime.intent":
			if s.Intent != nil {
				return s, errors.New("runtime intent already exists")
			}
			var intent Intent
			if err := canonical.Decode(e.Payload, &intent); err != nil {
				return s, err
			}
			i, err := runtime.NewInvocation(intent.Invocation.Profile, intent.Invocation.Input)
			if err != nil {
				return s, err
			}
			if intent.Invocation.Version != 1 || i.ID != intent.Invocation.ID || i.Profile.Runtime != "codex-app-server" || !filepath.IsAbs(intent.Directory) {
				return s, errors.New("invalid runtime intent")
			}
			s.Intent = &intent
		case "runtime.thread":
			if s.Intent == nil || s.Thread != nil {
				return s, errors.New("thread observation transition rejected")
			}
			var thread codexrpc.ThreadSettings
			if err := canonical.Decode(e.Payload, &thread); err != nil {
				return s, err
			}
			if err := thread.Validate(s.Intent.Invocation.Profile, s.Intent.Directory); err != nil {
				return s, err
			}
			s.Thread = &thread
		case "runtime.turn-intent":
			if s.Thread == nil || s.TurnPending {
				return s, errors.New("turn dispatch transition rejected")
			}
			var id struct {
				InvocationID string `json:"invocation_id"`
			}
			if err := canonical.Decode(e.Payload, &id); err != nil {
				return s, err
			}
			if id.InvocationID != s.Intent.Invocation.ID {
				return s, errors.New("turn invocation substitution")
			}
			s.TurnPending = true
		case "runtime.turn":
			if !s.TurnPending || s.TurnID != "" {
				return s, errors.New("turn observation transition rejected")
			}
			var turn struct {
				ID string `json:"id"`
			}
			if err := canonical.Decode(e.Payload, &turn); err != nil {
				return s, err
			}
			if turn.ID == "" || len(turn.ID) > 256 {
				return s, errors.New("invalid turn identity")
			}
			if s.UsageTurnID != "" && s.UsageTurnID != turn.ID {
				return s, errors.New("usage turn differs from dispatch receipt")
			}
			if s.ToolTurnID != "" && s.ToolTurnID != turn.ID {
				return s, errors.New("tool request turn differs from dispatch receipt")
			}
			s.TurnID = turn.ID
		case "runtime.turn-status":
			if s.TurnID == "" || s.Result != nil || s.PendingTool != nil {
				return s, errors.New("turn status transition rejected")
			}
			var observed TurnStatus
			if err := canonical.Decode(e.Payload, &observed); err != nil {
				return s, err
			}
			if observed.ID != s.TurnID {
				return s, errors.New("turn status identity mismatch")
			}
			switch observed.Status {
			case "inProgress", "completed", "failed", "interrupted":
			default:
				return s, errors.New("unknown turn status")
			}
			if s.TurnStatus != "" && s.TurnStatus != "inProgress" && observed.Status != s.TurnStatus {
				return s, errors.New("terminal provider state changed")
			}
			s.TurnStatus = observed.Status
		case "runtime.result":
			if s.TurnID == "" || s.Result != nil || s.RouteResumePending || s.RouteFailure != "" || s.UsagePending != nil || s.UsageFailure != "" {
				return s, errors.New("runtime result transition rejected")
			}
			var receipt TurnResult
			if err := canonical.Decode(e.Payload, &receipt); err != nil {
				return s, err
			}
			if receipt.TurnID != s.TurnID || s.TurnStatus != "completed" {
				return s, errors.New("result lacks exact completed turn observation")
			}
			result := receipt.Result
			if err := runtime.ValidateResult(s.Intent.Invocation, result, true); err != nil {
				return s, err
			}
			if result.ObservedProvider == nil || *result.ObservedProvider != s.Thread.Provider {
				return s, errors.New("result provider observation missing or substituted")
			}
			if (result.ObservedEffort == nil) != (s.Thread.Effort == nil) || result.ObservedEffort != nil && *result.ObservedEffort != *s.Thread.Effort {
				return s, errors.New("result effort observation substituted")
			}
			if err := s.validateUsageResult(result); err != nil {
				return s, err
			}
			s.Result = &result
		default:
			return s, errors.New("unknown runtime event")
		}
	}
	if s.Intent != nil {
		s.RouteEvidence = &RouteEvidence{DispatchObserved: s.Thread, ContinuationConfiguration: s.RouteObserved, ContinuationStatus: s.RouteFailure}
		if s.RouteResumePending {
			s.RouteEvidence.ContinuationStatus = "PENDING"
		} else if s.RouteResumes == 0 {
			s.RouteEvidence.ContinuationStatus = "NOT_ATTEMPTED"
		} else if s.RouteFailure == "" {
			s.RouteEvidence.ContinuationStatus = "MATCH"
		}
		s.RouteEvidence.NotificationCoverage = "NOT_PROVEN"
		if s.NotificationStreamComplete {
			s.RouteEvidence.NotificationCoverage = "THROUGH_TERMINAL"
		}
		s.RouteEvidence.HistoricalRouteStatus = "UNKNOWN"
		if s.Thread != nil {
			s.RouteEvidence.HistoricalRouteStatus = "OBSERVED_AT_DISPATCH"
		}
		if s.RouteFailure == "ROUTE_IDENTITY_CONTRADICTION" {
			s.RouteEvidence.HistoricalRouteStatus = "CONTRADICTION_OBSERVED"
		}
		s.ExecutionOutcome = "UNKNOWN"
		if s.TurnStatus != "" && s.TurnStatus != "inProgress" {
			s.ExecutionOutcome = s.TurnStatus
		}
	}
	return s, nil
}

// Inspect validates the runtime journal before returning continuation handles.
func Inspect(path string) (State, error) {
	events, err := journal.Read(path)
	if err != nil {
		return State{}, err
	}
	return replay(events)
}

// InspectWithHead returns state and journal identity from the same validated read.
// An empty journal has no head and cannot supply a completed execution receipt.
func InspectWithHead(path string) (State, string, error) {
	events, err := journal.Read(path)
	if err != nil {
		return State{}, "", err
	}
	s, err := replay(events)
	if err != nil {
		return State{}, "", err
	}
	if len(events) == 0 {
		return s, "", nil
	}
	return s, events[len(events)-1].Hash, nil
}

func appendEvent(path, kind string, payload any) error {
	_, err := journal.Append(path, kind, payload, func(events []journal.Event) error { _, err := replay(events); return err })
	return err
}
