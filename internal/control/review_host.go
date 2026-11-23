package control

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexhost"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
)

// ReviewHostIntent pins a private host to one candidate-bound invocation.
type ReviewHostIntent struct {
	Invocation runtime.Invocation `json:"invocation"`
	Launch     codexhost.Launch   `json:"launch"`
}

// ReviewHostState retains host preparation and observation without granting writes.
type ReviewHostState struct {
	RuntimeReceipt *ReviewRuntimeReceipt `json:"runtime_receipt,omitempty"`
	Intent         ReviewHostIntent      `json:"intent"`
	Ready          bool                  `json:"ready"`
	Receipt        *codexhost.Receipt    `json:"receipt"`
}

// ReviewRuntimeReceipt binds a completed runtime observation to its journal head.
type ReviewRuntimeReceipt struct {
	InvocationID string `json:"invocation_id"`
	ThreadID     string `json:"thread_id"`
	TurnID       string `json:"turn_id"`
	JournalHead  string `json:"journal_head"`
	ResultHash   string `json:"result_hash"`
	UsagePending bool   `json:"usage_pending,omitempty"`
}

// ScheduledReviewHostEvent retains correction-turn host transitions separately
// from the original singleton reviewer receipt.
type ScheduledReviewHostEvent struct {
	TaskID         string                `json:"task_id"`
	Intent         *ReviewHostIntent     `json:"intent,omitempty"`
	HostReceipt    *codexhost.Receipt    `json:"host_receipt,omitempty"`
	RuntimeReceipt *ReviewRuntimeReceipt `json:"runtime_receipt,omitempty"`
}

func expectedReviewHost(s Snapshot) (ReviewHostIntent, error) {
	i, err := reviewInvocation(s)
	if err != nil {
		return ReviewHostIntent{}, err
	}
	return expectedReviewHostForInvocation(s, i)
}

func expectedReviewHostForInvocation(s Snapshot, i runtime.Invocation) (ReviewHostIntent, error) {
	c := s.Creation.Config.Codex
	if i.Profile.Runtime != "codex-app-server" || c == nil {
		return ReviewHostIntent{}, errors.New("configured Codex review required")
	}
	rel, err := filepath.Rel(s.Creation.Repository.Root, c.StateRoot)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ReviewHostIntent{}, errors.New("review host state must be outside source repository")
	}
	l, err := codexhost.Expected(filepath.Join(c.StateRoot, s.RunID, "review-"+i.ID), c.Executable, c.ExecutableHash)
	return ReviewHostIntent{i, l}, err
}

func expectedScheduledReviewHost(s Snapshot, taskID string) (ReviewHostState, error) {
	correction, ok := scheduledCorrectionForTask(s, taskID)
	if !ok || correction.Invocation.Profile.Role != "reviewer" || correction.ScheduledTaskID != taskID {
		return ReviewHostState{}, ErrAutonomousUnsafe
	}
	invocation, err := scheduledTurnInvocation(correction.Invocation, taskscheduler.OperationReviewer, taskID)
	if err != nil {
		return ReviewHostState{}, err
	}
	intent, err := expectedReviewHostForInvocation(s, invocation)
	if err != nil {
		return ReviewHostState{}, err
	}
	return ReviewHostState{Intent: intent}, nil
}

func scheduledReviewHostState(s Snapshot, taskID string) *ReviewHostState {
	if taskID == "" {
		return s.ReviewHost
	}
	state, ok := s.ScheduledReviewHosts[taskID]
	if !ok {
		return nil
	}
	return &state
}

func appendReviewHostTransition(path, taskID, kind string, payload any) error {
	if taskID == "" {
		return Append(path, kind, payload)
	}
	event := ScheduledReviewHostEvent{TaskID: taskID}
	var scheduledKind string
	switch kind {
	case "review.host-intent", "review.host-ready":
		intent, ok := payload.(ReviewHostIntent)
		if !ok {
			return errors.New("scheduled review host intent payload mismatch")
		}
		event.Intent = &intent
		scheduledKind = "scheduled." + kind
	case "review.host-observed":
		receipt, ok := payload.(codexhost.Receipt)
		if !ok {
			return errors.New("scheduled review host receipt payload mismatch")
		}
		event.HostReceipt = &receipt
		scheduledKind = "scheduled." + kind
	case "review.runtime-observed":
		receipt, ok := payload.(ReviewRuntimeReceipt)
		if !ok {
			return errors.New("scheduled review runtime receipt payload mismatch")
		}
		event.RuntimeReceipt = &receipt
		scheduledKind = "scheduled." + kind
	default:
		return errors.New("unsupported scheduled review host transition")
	}
	return Append(path, scheduledKind, event)
}

func replayScheduledReviewHost(s *Snapshot, e journal.Event) error {
	var event ScheduledReviewHostEvent
	if err := canonical.Decode(e.Payload, &event); err != nil {
		return err
	}
	if event.TaskID == "" {
		return ErrAutonomousUnsafe
	}
	expected, err := expectedScheduledReviewHost(*s, event.TaskID)
	if err != nil {
		return err
	}
	if s.ScheduledReviewHosts == nil {
		s.ScheduledReviewHosts = make(map[string]ReviewHostState)
	}
	host, exists := s.ScheduledReviewHosts[event.TaskID]
	switch e.Kind {
	case "scheduled.review.host-intent":
		if exists || event.Intent == nil || *event.Intent != expected.Intent || event.HostReceipt != nil || event.RuntimeReceipt != nil {
			return errors.New("scheduled review host intent substitution or duplication")
		}
		host = expected
	case "scheduled.review.host-ready":
		if !exists || host.Ready || host.Intent != expected.Intent || event.Intent == nil || *event.Intent != expected.Intent || event.HostReceipt != nil || event.RuntimeReceipt != nil {
			return errors.New("scheduled review host preparation transition rejected")
		}
		host.Ready = true
	case "scheduled.review.host-observed":
		if !exists || !host.Ready || host.Intent != expected.Intent || event.HostReceipt == nil || event.Intent != nil || event.RuntimeReceipt != nil {
			return errors.New("scheduled review host observation transition rejected")
		}
		if err := validateCodexHostReceipt(*event.HostReceipt, expected.Intent.Launch, s.Creation.Config.Codex); err != nil {
			return err
		}
		host.Receipt = event.HostReceipt
	case "scheduled.review.runtime-observed":
		if !exists || !host.Ready || host.Receipt == nil || host.Intent != expected.Intent || host.RuntimeReceipt != nil || event.RuntimeReceipt == nil || event.Intent != nil || event.HostReceipt != nil {
			return errors.New("scheduled review runtime receipt transition rejected")
		}
		receipt := event.RuntimeReceipt
		if receipt.InvocationID != expected.Intent.Invocation.ID || strings.TrimSpace(receipt.ThreadID) == "" || len(receipt.ThreadID) > 256 || strings.TrimSpace(receipt.TurnID) == "" || len(receipt.TurnID) > 256 {
			return errors.New("scheduled review runtime receipt identity mismatch")
		}
		for _, digest := range []string{receipt.JournalHead, receipt.ResultHash} {
			if err := safepath.RequireDigest(digest); err != nil {
				return err
			}
		}
		host.RuntimeReceipt = receipt
	default:
		return errors.New("unsupported scheduled review host event")
	}
	s.ScheduledReviewHosts[event.TaskID] = host
	return nil
}

func replayReviewHost(s *Snapshot, e journal.Event) error {
	expected, err := expectedReviewHost(*s)
	if err != nil {
		return err
	}
	switch e.Kind {
	case "review.runtime-observed":
		if s.ReviewHost == nil || !s.ReviewHost.Ready || s.ReviewHost.Receipt == nil || s.ReviewHost.Intent != expected || s.ReviewHost.RuntimeReceipt != nil {
			return errors.New("review runtime receipt transition rejected")
		}
		var receipt ReviewRuntimeReceipt
		if err := canonical.Decode(e.Payload, &receipt); err != nil {
			return err
		}
		if receipt.InvocationID != expected.Invocation.ID || strings.TrimSpace(receipt.ThreadID) == "" || len(receipt.ThreadID) > 256 || strings.TrimSpace(receipt.TurnID) == "" || len(receipt.TurnID) > 256 {
			return errors.New("review runtime receipt identity mismatch")
		}
		for _, digest := range []string{receipt.JournalHead, receipt.ResultHash} {
			if err := safepath.RequireDigest(digest); err != nil {
				return err
			}
		}
		s.ReviewHost.RuntimeReceipt = &receipt
	case "review.host-intent":
		var intent ReviewHostIntent
		if err := canonical.Decode(e.Payload, &intent); err != nil {
			return err
		}
		if intent != expected || s.ReviewHost != nil && s.ReviewHost.Intent.Invocation.ID == intent.Invocation.ID {
			return errors.New("review host intent substitution or duplication")
		}
		s.ReviewHost = &ReviewHostState{Intent: intent}
	case "review.host-ready":
		var intent ReviewHostIntent
		if err := canonical.Decode(e.Payload, &intent); err != nil {
			return err
		}
		if s.ReviewHost == nil || s.ReviewHost.Ready || s.ReviewHost.Intent != expected || intent != expected {
			return errors.New("review host preparation transition rejected")
		}
		s.ReviewHost.Ready = true
	case "review.host-observed":
		if s.ReviewHost == nil || !s.ReviewHost.Ready || s.ReviewHost.Intent != expected {
			return errors.New("review host observation transition rejected")
		}
		var receipt codexhost.Receipt
		if err := canonical.Decode(e.Payload, &receipt); err != nil {
			return err
		}
		if err := validateCodexHostReceipt(receipt, expected.Launch, s.Creation.Config.Codex); err != nil {
			return err
		}
		s.ReviewHost.Receipt = &receipt
	}
	return nil
}
