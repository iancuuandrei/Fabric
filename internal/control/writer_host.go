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

// WriterHostIntent pins a private host to one candidate-bound invocation.
type WriterHostIntent struct {
	Invocation runtime.Invocation `json:"invocation"`
	Launch     codexhost.Launch   `json:"launch"`
}

// WriterHostState retains host preparation and observation without granting writes.
type WriterHostState struct {
	RuntimeReceipt *WriterRuntimeReceipt `json:"runtime_receipt,omitempty"`
	Intent         WriterHostIntent      `json:"intent"`
	Ready          bool                  `json:"ready"`
	Receipt        *codexhost.Receipt    `json:"receipt"`
}

// WriterRuntimeReceipt binds a completed runtime observation to its journal head.
type WriterRuntimeReceipt struct {
	InvocationID string `json:"invocation_id"`
	ThreadID     string `json:"thread_id"`
	TurnID       string `json:"turn_id"`
	JournalHead  string `json:"journal_head"`
	ResultHash   string `json:"result_hash"`
}

func expectedWriterHost(s Snapshot) (WriterHostIntent, error) {
	i, err := writerInvocation(s)
	if err != nil {
		return WriterHostIntent{}, err
	}
	c := s.Creation.Config.Codex
	if i.Profile.Runtime != "codex-app-server" || c == nil {
		return WriterHostIntent{}, errors.New("configured Codex writer required")
	}
	rel, err := filepath.Rel(s.Creation.Repository.Root, c.StateRoot)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return WriterHostIntent{}, errors.New("writer host state must be outside source repository")
	}
	l, err := codexhost.Expected(filepath.Join(c.StateRoot, s.RunID, "writer-"+i.ID), c.Executable, c.ExecutableHash)
	return WriterHostIntent{i, l}, err
}

func replayWriterHost(s *Snapshot, e journal.Event) error {
	expected, err := expectedWriterHost(*s)
	if err != nil {
		return err
	}
	switch e.Kind {
	case "writer.runtime-observed":
		if s.WriterHost == nil || !s.WriterHost.Ready || s.WriterHost.Receipt == nil || s.WriterHost.Intent != expected || s.WriterHost.RuntimeReceipt != nil {
			return errors.New("writer runtime receipt transition rejected")
		}
		var receipt WriterRuntimeReceipt
		if err := canonical.Decode(e.Payload, &receipt); err != nil {
			return err
		}
		if receipt.InvocationID != expected.Invocation.ID || strings.TrimSpace(receipt.ThreadID) == "" || len(receipt.ThreadID) > 256 || strings.TrimSpace(receipt.TurnID) == "" || len(receipt.TurnID) > 256 {
			return errors.New("writer runtime receipt identity mismatch")
		}
		for _, digest := range []string{receipt.JournalHead, receipt.ResultHash} {
			if err := safepath.RequireDigest(digest); err != nil {
				return err
			}
		}
		s.WriterHost.RuntimeReceipt = &receipt
	case "writer.host-intent":
		var intent WriterHostIntent
		if err := canonical.Decode(e.Payload, &intent); err != nil {
			return err
		}
		if intent != expected || s.WriterHost != nil && s.WriterHost.Intent.Invocation.ID == intent.Invocation.ID {
			return errors.New("writer host intent substitution or duplication")
		}
		s.WriterHost = &WriterHostState{Intent: intent}
	case "writer.host-ready":
		var intent WriterHostIntent
		if err := canonical.Decode(e.Payload, &intent); err != nil {
			return err
		}
		if s.WriterHost == nil || s.WriterHost.Ready || s.WriterHost.Intent != expected || intent != expected {
			return errors.New("writer host preparation transition rejected")
		}
		s.WriterHost.Ready = true
	case "writer.host-observed":
		if s.WriterHost == nil || !s.WriterHost.Ready || s.WriterHost.Intent != expected {
			return errors.New("writer host observation transition rejected")
		}
		var receipt codexhost.Receipt
		if err := canonical.Decode(e.Payload, &receipt); err != nil {
			return err
		}
		if err := validateCodexHostReceipt(receipt, expected.Launch, s.Creation.Config.Codex); err != nil {
			return err
		}
		s.WriterHost.Receipt = &receipt
	}
	return nil
}
