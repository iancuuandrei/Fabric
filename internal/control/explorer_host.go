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
)

// ExplorerHostIntent pins a private host to one candidate-bound invocation.
type ExplorerHostIntent struct {
	Question   string             `json:"question"`
	Invocation runtime.Invocation `json:"invocation"`
	Launch     codexhost.Launch   `json:"launch"`
}

// ExplorerHostState retains host preparation and observation without granting writes.
type ExplorerHostState struct {
	RuntimeReceipt *ExplorerRuntimeReceipt `json:"runtime_receipt,omitempty"`
	Intent         ExplorerHostIntent      `json:"intent"`
	Ready          bool                    `json:"ready"`
	Receipt        *codexhost.Receipt      `json:"receipt"`
}

// ExplorerRuntimeReceipt binds a completed runtime observation to its journal head.
type ExplorerRuntimeReceipt struct {
	FailureCode  string `json:"failure_code,omitempty"`
	InvocationID string `json:"invocation_id"`
	ThreadID     string `json:"thread_id"`
	TurnID       string `json:"turn_id"`
	JournalHead  string `json:"journal_head"`
	ResultHash   string `json:"result_hash"`
	UsagePending bool   `json:"usage_pending,omitempty"`
}

func expectedExplorerHost(s Snapshot, question string) (ExplorerHostIntent, error) {
	i, err := explorerInvocation(s, question)
	if err != nil {
		return ExplorerHostIntent{}, err
	}
	return expectedExplorerHostForInvocation(s, question, i)
}

func expectedExplorerHostForInvocation(s Snapshot, question string, observed runtime.Invocation) (ExplorerHostIntent, error) {
	base, err := explorerInvocation(s, question)
	if err != nil {
		return ExplorerHostIntent{}, err
	}
	i, err := resolveScheduledRecordedInvocation(s, base, observed)
	if err != nil {
		return ExplorerHostIntent{}, err
	}
	c := s.Creation.Config.Codex
	if i.Profile.Runtime != "codex-app-server" || c == nil {
		return ExplorerHostIntent{}, errors.New("configured Codex explorer required")
	}
	rel, err := filepath.Rel(s.Creation.Repository.Root, c.StateRoot)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ExplorerHostIntent{}, errors.New("explorer host state must be outside source repository")
	}
	l, err := codexhost.Expected(filepath.Join(c.StateRoot, s.RunID, "explorer-"+i.ID), c.Executable, c.ExecutableHash)
	return ExplorerHostIntent{question, i, l}, err
}

func explorerRunForInvocation(s Snapshot, invocationID string) (*ExplorerHostState, bool) {
	if s.ExplorerRuns != nil {
		if run, ok := s.ExplorerRuns[invocationID]; ok {
			copy := run
			return &copy, true
		}
	}
	if s.ExplorerHost != nil && s.ExplorerHost.Intent.Invocation.ID == invocationID {
		copy := *s.ExplorerHost
		return &copy, true
	}
	return nil, false
}

func putExplorerRun(s *Snapshot, state ExplorerHostState) {
	id := state.Intent.Invocation.ID
	if s.ExplorerRuns == nil {
		s.ExplorerRuns = map[string]ExplorerHostState{}
	}
	s.ExplorerRuns[id] = state
	// Retain legacy singleton for backward compatibility: first run mirrors
	// the singleton so old journals and single-explorer flows replay
	// identically. Parallel graph runs keep every invocation in the map;
	// the singleton remains the first intent and is never substituted by a
	// sibling cohort invocation.
	if s.ExplorerHost == nil {
		copy := state
		s.ExplorerHost = &copy
	} else if s.ExplorerHost.Intent.Invocation.ID == id {
		copy := state
		s.ExplorerHost = &copy
	}
}

func replayExplorerHost(s *Snapshot, e journal.Event) error {
	// Graph-bound parallel runs keep per-invocation state in ExplorerRuns.
	// Legacy sequential runs retain the singleton behavior byte-for-byte.
	if s.Creation.Execution != nil && s.Creation.Execution.GraphEnabled() {
		return replayExplorerHostParallel(s, e)
	}
	question := ""
	if e.Kind == "explorer.host-intent" {
		var proposed ExplorerHostIntent
		if err := canonical.Decode(e.Payload, &proposed); err != nil {
			return err
		}
		question = proposed.Question
	} else if s.ExplorerHost != nil {
		question = s.ExplorerHost.Intent.Question
	}
	expected, err := expectedExplorerHost(*s, question)
	if err != nil {
		return err
	}
	switch e.Kind {
	case "explorer.runtime-observed":
		if s.ExplorerHost == nil || !s.ExplorerHost.Ready || s.ExplorerHost.Receipt == nil || s.ExplorerHost.Intent != expected || s.ExplorerHost.RuntimeReceipt != nil {
			return errors.New("explorer runtime receipt transition rejected")
		}
		var receipt ExplorerRuntimeReceipt
		if err := canonical.Decode(e.Payload, &receipt); err != nil {
			return err
		}
		if receipt.FailureCode != "" && (receipt.FailureCode != "serverOverloaded" || receipt.UsagePending || s.Creation.Config.Version != 1) {
			return errors.New("invalid explorer failure receipt")
		}
		if receipt.InvocationID != expected.Invocation.ID || strings.TrimSpace(receipt.ThreadID) == "" || len(receipt.ThreadID) > 256 || strings.TrimSpace(receipt.TurnID) == "" || len(receipt.TurnID) > 256 {
			return errors.New("explorer runtime receipt identity mismatch")
		}
		for _, digest := range []string{receipt.JournalHead, receipt.ResultHash} {
			if err := safepath.RequireDigest(digest); err != nil {
				return err
			}
		}
		s.ExplorerHost.RuntimeReceipt = &receipt
		putExplorerRun(s, *s.ExplorerHost)
	case "explorer.host-intent":
		var intent ExplorerHostIntent
		if err := canonical.Decode(e.Payload, &intent); err != nil {
			return err
		}
		if intent != expected || s.ExplorerHost != nil && s.ExplorerHost.Intent.Invocation.ID == intent.Invocation.ID {
			return errors.New("explorer host intent substitution or duplication")
		}
		s.ExplorerHost = &ExplorerHostState{Intent: intent}
		putExplorerRun(s, *s.ExplorerHost)
	case "explorer.host-ready":
		var intent ExplorerHostIntent
		if err := canonical.Decode(e.Payload, &intent); err != nil {
			return err
		}
		if s.ExplorerHost == nil || s.ExplorerHost.Ready || s.ExplorerHost.Intent != expected || intent != expected {
			return errors.New("explorer host preparation transition rejected")
		}
		s.ExplorerHost.Ready = true
		putExplorerRun(s, *s.ExplorerHost)
	case "explorer.host-observed":
		if s.ExplorerHost == nil || !s.ExplorerHost.Ready || s.ExplorerHost.Intent != expected {
			return errors.New("explorer host observation transition rejected")
		}
		var receipt codexhost.Receipt
		if err := canonical.Decode(e.Payload, &receipt); err != nil {
			return err
		}
		if err := validateCodexHostReceipt(receipt, expected.Launch, s.Creation.Config.Codex); err != nil {
			return err
		}
		s.ExplorerHost.Receipt = &receipt
		putExplorerRun(s, *s.ExplorerHost)
	}
	return nil
}

func replayExplorerHostParallel(s *Snapshot, e journal.Event) error {
	switch e.Kind {
	case "explorer.host-intent":
		var intent ExplorerHostIntent
		if err := canonical.Decode(e.Payload, &intent); err != nil {
			return err
		}
		expected, err := expectedExplorerHostForInvocation(*s, intent.Question, intent.Invocation)
		if err != nil {
			return err
		}
		if intent != expected {
			return errors.New("explorer host intent substitution or duplication")
		}
		if _, exists := explorerRunForInvocation(*s, intent.Invocation.ID); exists {
			return errors.New("explorer host intent substitution or duplication")
		}
		putExplorerRun(s, ExplorerHostState{Intent: intent})
	case "explorer.host-ready":
		var intent ExplorerHostIntent
		if err := canonical.Decode(e.Payload, &intent); err != nil {
			return err
		}
		run, ok := explorerRunForInvocation(*s, intent.Invocation.ID)
		if !ok || run.Ready {
			return errors.New("explorer host preparation transition rejected")
		}
		expected, err := expectedExplorerHostForInvocation(*s, intent.Question, intent.Invocation)
		if err != nil || intent != expected || run.Intent != expected {
			return errors.New("explorer host preparation transition rejected")
		}
		run.Ready = true
		putExplorerRun(s, *run)
	case "explorer.host-observed":
		var receipt codexhost.Receipt
		if err := canonical.Decode(e.Payload, &receipt); err != nil {
			return err
		}
		// Bind exact invocation via launch identity; no sibling substituted.
		matched := ""
		for id, run := range s.ExplorerRuns {
			if run.Intent.Launch.Root != "" {
				wantID, err := run.Intent.Launch.BindingID()
				if err == nil && wantID == receipt.LaunchID && run.Ready && run.Receipt == nil {
					matched = id
					break
				}
			}
		}
		if matched == "" && s.ExplorerHost != nil && s.ExplorerHost.Ready && s.ExplorerHost.Receipt == nil {
			if wantID, err := s.ExplorerHost.Intent.Launch.BindingID(); err == nil && wantID == receipt.LaunchID {
				matched = s.ExplorerHost.Intent.Invocation.ID
			}
		}
		if matched == "" {
			return errors.New("explorer host observation transition rejected")
		}
		run, _ := explorerRunForInvocation(*s, matched)
		expected, err := expectedExplorerHostForInvocation(*s, run.Intent.Question, run.Intent.Invocation)
		if err != nil || run.Intent != expected {
			return errors.New("explorer host observation transition rejected")
		}
		if err := validateCodexHostReceipt(receipt, expected.Launch, s.Creation.Config.Codex); err != nil {
			return err
		}
		run.Receipt = &receipt
		putExplorerRun(s, *run)
	case "explorer.runtime-observed":
		var receipt ExplorerRuntimeReceipt
		if err := canonical.Decode(e.Payload, &receipt); err != nil {
			return err
		}
		run, ok := explorerRunForInvocation(*s, receipt.InvocationID)
		if !ok || !run.Ready || run.Receipt == nil || run.RuntimeReceipt != nil {
			return errors.New("explorer runtime receipt transition rejected")
		}
		expected, err := expectedExplorerHostForInvocation(*s, run.Intent.Question, run.Intent.Invocation)
		if err != nil || run.Intent != expected {
			return errors.New("explorer runtime receipt transition rejected")
		}
		if receipt.FailureCode != "" && (receipt.FailureCode != "serverOverloaded" || receipt.UsagePending || s.Creation.Config.Version != 1) {
			return errors.New("invalid explorer failure receipt")
		}
		if receipt.InvocationID != expected.Invocation.ID || strings.TrimSpace(receipt.ThreadID) == "" || len(receipt.ThreadID) > 256 || strings.TrimSpace(receipt.TurnID) == "" || len(receipt.TurnID) > 256 {
			return errors.New("explorer runtime receipt identity mismatch")
		}
		for _, digest := range []string{receipt.JournalHead, receipt.ResultHash} {
			if err := safepath.RequireDigest(digest); err != nil {
				return err
			}
		}
		run.RuntimeReceipt = &receipt
		putExplorerRun(s, *run)
	}
	return nil
}
