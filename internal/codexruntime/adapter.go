package codexruntime

import (
	"context"
	"errors"
	"os"
	"sync"

	"harness.local/engorch/internal/codexrpc"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
)

// Adapter owns one initialized server connection and one durable invocation.
// A fresh connection may observe the same journal via Resume after interruption.
// Provisioning and tool-isolation qualification are mandatory caller concerns.
type Adapter struct {
	UnlimitedTokens  bool
	UsageBudget      int64
	RequireLiveUsage bool
	UsageQualified   bool
	Candidate        *CandidateBinding
	Client           *codexrpc.Client
	JournalPath      string
	Directory        string
	Source           *repository.Identity
	RI               *RIBinding
	Lexical          *LexicalBinding
	mu               sync.Mutex
	usage            runtime.Usage
}

var _ runtime.AgentRuntime = (*Adapter)(nil)

// Capabilities reports implemented protocol behavior, not certified isolation.
func (*Adapter) Capabilities() runtime.Capabilities {
	return runtime.Capabilities{ModelSelection: true, EffortSelection: true, Continuation: true, ObservedModel: true}
}

func (a *Adapter) lease() (func(), error) {
	if a.Client == nil || a.JournalPath == "" {
		return nil, errors.New("initialized client and runtime journal required")
	}
	f, err := os.OpenFile(a.JournalPath+".execution", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(a.JournalPath + ".execution") }, nil
}

// Execute persists intent before each provider effect. Repeated Execute calls
// cannot create another thread or turn from an uncertain existing invocation.
func (a *Adapter) Execute(ctx context.Context, i runtime.Invocation) (executionResult runtime.Result, executionErr error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return runtime.Result{}, err
	}
	if a.UnlimitedTokens && a.UsageBudget != 0 {
		return runtime.Result{}, errors.New("unlimited token policy requires zero numeric reservation")
	}
	if a.RequireLiveUsage && (!a.UsageQualified || a.UsageBudget < 1 && !a.UnlimitedTokens) {
		return runtime.Result{}, errors.New("LIVE_USAGE_NOT_QUALIFIED")
	}
	if err := a.validateLexicalAdmission(); err != nil {
		return runtime.Result{}, err
	}
	release, err := a.lease()
	if err != nil {
		return runtime.Result{}, err
	}
	defer release()
	defer func() {
		if errors.Is(executionErr, codexrpc.ErrRouteIdentityContradiction) {
			state, inspectErr := Inspect(a.JournalPath)
			if inspectErr == nil && state.Intent != nil && state.Result == nil {
				executionErr = errors.Join(executionErr, appendEvent(a.JournalPath, "runtime.route-contradiction", routeFailureEvidence(executionErr, nil)))
			}
		}
	}()
	if err := appendEvent(a.JournalPath, "runtime.intent", Intent{i, a.Directory}); err != nil {
		return runtime.Result{}, err
	}
	if a.Source != nil {
		if err := appendEvent(a.JournalPath, "runtime.source", *a.Source); err != nil {
			return runtime.Result{}, err
		}
	}
	if a.RI != nil {
		if err := appendEvent(a.JournalPath, "runtime.ri", *a.RI); err != nil {
			return runtime.Result{}, err
		}
	}
	if a.Candidate != nil {
		if err := appendEvent(a.JournalPath, "runtime.candidate", *a.Candidate); err != nil {
			return runtime.Result{}, err
		}
	}
	if a.Lexical != nil {
		candidate := ""
		if a.Candidate != nil {
			candidate, err = a.Candidate.Candidate.ID()
			if err != nil {
				return runtime.Result{}, err
			}
		}
		record, err := a.Lexical.Record(*a.Source, candidate)
		if err != nil {
			return runtime.Result{}, err
		}
		if err := appendEvent(a.JournalPath, "runtime.lexical-record", record); err != nil {
			return runtime.Result{}, err
		}
	}
	a.Client.SetObserver(a.observeUsage)
	thread, err := a.Client.StartThreadWithTools(ctx, i.Profile, a.Directory, a.SourceTools())
	if err != nil {
		return runtime.Result{}, err
	}
	if err := appendEvent(a.JournalPath, "runtime.thread", thread); err != nil {
		return runtime.Result{}, err
	}
	if err := a.beginUsage(thread); err != nil {
		return runtime.Result{}, err
	}
	if err := appendEvent(a.JournalPath, "runtime.turn-intent", struct {
		InvocationID string `json:"invocation_id"`
	}{i.ID}); err != nil {
		return runtime.Result{}, err
	}
	turn, events, err := a.Client.StartTurn(ctx, thread, i)
	if err != nil {
		return runtime.Result{}, err
	}
	if err := appendEvent(a.JournalPath, "runtime.turn", struct {
		ID string `json:"id"`
	}{turn.ID}); err != nil {
		return runtime.Result{}, err
	}
	if err := appendEvent(a.JournalPath, "runtime.usage-start", UsageStart{turn.ID}); err != nil {
		return runtime.Result{}, err
	}
	for _, event := range events {
		if err := a.recordTerminalResponse(event, thread.ThreadID, turn.ID); err != nil {
			return runtime.Result{}, err
		}
		completed, err := codexrpc.Completion(event, thread.ThreadID, turn.ID)
		if err != nil {
			return runtime.Result{}, err
		}
		if completed != nil {
			turn = *completed
		}
	}
	bytes, count := 0, 0
	for turn.Status == "inProgress" {
		event, err := a.Client.Receive(ctx)
		if err != nil {
			return runtime.Result{}, err
		}
		if err := a.recordTerminalResponse(event, thread.ThreadID, turn.ID); err != nil {
			return runtime.Result{}, err
		}
		bytes += len(event.Params)
		count++
		if bytes > 16*codexrpc.MaxMessage || count > 8192 {
			_ = a.Client.Close()
			return runtime.Result{}, errors.New("runtime event stream exceeds bounds")
		}
		completed, err := codexrpc.Completion(event, thread.ThreadID, turn.ID)
		if err != nil {
			return runtime.Result{}, err
		}
		if completed != nil {
			turn = *completed
		}
	}
	if err := appendEvent(a.JournalPath, "runtime.stream-terminal", TurnStatus{turn.ID, turn.Status}); err != nil {
		return runtime.Result{}, err
	}
	if turn.Status == "completed" && turn.ItemsView != "" && turn.ItemsView != "full" {
		if err := appendEvent(a.JournalPath, "runtime.turn-status", TurnStatus{turn.ID, turn.Status}); err != nil {
			return runtime.Result{}, err
		}
		turn, err = a.Client.ReadTurn(ctx, thread, turn.ID)
		if err != nil {
			return runtime.Result{}, err
		}
	}
	return a.finish(i, thread, turn)
}

func (a *Adapter) finish(i runtime.Invocation, thread codexrpc.ThreadSettings, turn codexrpc.Turn) (runtime.Result, error) {
	if err := appendEvent(a.JournalPath, "runtime.turn-status", TurnStatus{turn.ID, turn.Status}); err != nil {
		return runtime.Result{}, err
	}
	result, err := turn.Result(i, thread)
	if err != nil {
		return result, err
	}
	if err := a.attachUsage(&result); err != nil {
		return runtime.Result{}, err
	}
	if err := appendEvent(a.JournalPath, "runtime.result", TurnResult{turn.ID, result}); err != nil {
		return result, err
	}
	a.usage = result.Usage
	return result, nil
}

// Resume reloads and reattests the recorded thread, then reads its exact turn.
// It starts no generation and returns
// an explicit error while work is active, absent, failed or incompletely observed.
func (a *Adapter) Resume(ctx context.Context, invocationID string) (runtime.Result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	release, err := a.lease()
	if err != nil {
		return runtime.Result{}, err
	}
	defer release()
	s, err := Inspect(a.JournalPath)
	if err != nil {
		return runtime.Result{}, err
	}
	if s.Intent == nil || s.Intent.Invocation.ID != invocationID || s.Intent.Directory != a.Directory {
		return runtime.Result{}, errors.New("continuation identity mismatch")
	}
	if (s.Source == nil) != (a.Source == nil) || s.Source != nil && *s.Source != *a.Source {
		return runtime.Result{}, errors.New("continuation source mismatch")
	}
	if (s.RI == nil) != (a.RI == nil) || s.RI != nil && *s.RI != *a.RI {
		return runtime.Result{}, errors.New("continuation RI mismatch")
	}
	if (s.Candidate == nil) != (a.Candidate == nil) || s.Candidate != nil && *s.Candidate != *a.Candidate {
		return runtime.Result{}, errors.New("runtime candidate binding changed")
	}
	if err := lexicalContinuation(s, a.Lexical); err != nil {
		return runtime.Result{}, err
	}
	if s.UsagePending != nil || s.UsageFailure != "" {
		return runtime.Result{}, errors.New("usage reconciliation blocked; no replay permitted")
	}

	if s.Result != nil {
		a.usage = s.Result.Usage
		return *s.Result, nil
	}
	if s.RouteFailure == "CONTINUATION_ROUTE_MISMATCH" {
		return runtime.Result{}, codexrpc.ErrContinuationRouteMismatch
	}
	if s.RouteFailure == "ROUTE_IDENTITY_CONTRADICTION" {
		return runtime.Result{}, codexrpc.ErrRouteIdentityContradiction
	}
	if s.Thread == nil || s.TurnID == "" {
		return runtime.Result{}, errors.New("provider dispatch UNKNOWN; missing durable continuation handle")
	}
	if !s.RouteResumePending {
		if err := appendEvent(a.JournalPath, "runtime.route-resume-intent", *s.Thread); err != nil {
			return runtime.Result{}, err
		}
	}
	a.Client.SetObserver(a.observeUsage)
	observed, err := a.Client.ResumeThread(ctx, *s.Thread)
	if err != nil {
		recordErr := appendEvent(a.JournalPath, "runtime.route-resume-failed", routeFailureEvidence(err, &observed))
		return runtime.Result{}, errors.Join(err, recordErr)
	}
	if err := appendEvent(a.JournalPath, "runtime.route-resumed", observed); err != nil {
		return runtime.Result{}, err
	}
	turn, err := a.Client.ReadTurn(ctx, *s.Thread, s.TurnID)
	if err != nil {
		if errors.Is(err, codexrpc.ErrRouteIdentityContradiction) {
			err = errors.Join(err, appendEvent(a.JournalPath, "runtime.route-contradiction", routeFailureEvidence(err, nil)))
		}
		return runtime.Result{}, err
	}
	return a.finish(s.Intent.Invocation, *s.Thread, turn)
}

// Cancel cannot target an active asynchronous operation in this serial adapter.
// Execute context cancellation closes the transport and leaves durable uncertainty.
func (*Adapter) Cancel(context.Context, string) error {
	return errors.New("targeted cancellation unsupported; cancel Execute context")
}

// Close releases the owned transport, including an in-flight blocked exchange.
func (a *Adapter) Close() error {
	if a.Client == nil {
		return nil
	}
	return a.Client.Close()
}

// Usage returns the last admitted provider accounting, with unavailable values null.
func (a *Adapter) Usage() runtime.Usage { a.mu.Lock(); defer a.mu.Unlock(); return a.usage }

func routeFailureEvidence(err error, observed *codexrpc.ThreadSettings) RouteFailureEvidence {
	evidence := RouteFailureEvidence{Reason: "UNKNOWN", Observed: observed}
	if errors.Is(err, codexrpc.ErrRouteIdentityContradiction) {
		evidence.Reason = "ROUTE_IDENTITY_CONTRADICTION"
	}
	if errors.Is(err, codexrpc.ErrContinuationRouteMismatch) {
		evidence.Reason = "CONTINUATION_ROUTE_MISMATCH"
	}
	var reroute *codexrpc.RerouteError
	if errors.As(err, &reroute) {
		evidence.Reroute = &reroute.Event
	}
	return evidence
}
