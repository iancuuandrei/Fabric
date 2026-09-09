package control

import (
	"errors"
	"path/filepath"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexhost"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/safepath"
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
}

func expectedReviewHost(s Snapshot) (ReviewHostIntent, error) {
	i, err := reviewInvocation(s)
	if err != nil {
		return ReviewHostIntent{}, err
	}
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
