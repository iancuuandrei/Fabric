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
	InvocationID string `json:"invocation_id"`
	ThreadID     string `json:"thread_id"`
	TurnID       string `json:"turn_id"`
	JournalHead  string `json:"journal_head"`
	ResultHash   string `json:"result_hash"`
}

func expectedExplorerHost(s Snapshot, question string) (ExplorerHostIntent, error) {
	i, err := explorerInvocation(s, question)
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

func replayExplorerHost(s *Snapshot, e journal.Event) error {
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
		if receipt.InvocationID != expected.Invocation.ID || strings.TrimSpace(receipt.ThreadID) == "" || len(receipt.ThreadID) > 256 || strings.TrimSpace(receipt.TurnID) == "" || len(receipt.TurnID) > 256 {
			return errors.New("explorer runtime receipt identity mismatch")
		}
		for _, digest := range []string{receipt.JournalHead, receipt.ResultHash} {
			if err := safepath.RequireDigest(digest); err != nil {
				return err
			}
		}
		s.ExplorerHost.RuntimeReceipt = &receipt
	case "explorer.host-intent":
		var intent ExplorerHostIntent
		if err := canonical.Decode(e.Payload, &intent); err != nil {
			return err
		}
		if intent != expected || s.ExplorerHost != nil && s.ExplorerHost.Intent.Invocation.ID == intent.Invocation.ID {
			return errors.New("explorer host intent substitution or duplication")
		}
		s.ExplorerHost = &ExplorerHostState{Intent: intent}
	case "explorer.host-ready":
		var intent ExplorerHostIntent
		if err := canonical.Decode(e.Payload, &intent); err != nil {
			return err
		}
		if s.ExplorerHost == nil || s.ExplorerHost.Ready || s.ExplorerHost.Intent != expected || intent != expected {
			return errors.New("explorer host preparation transition rejected")
		}
		s.ExplorerHost.Ready = true
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
	}
	return nil
}
