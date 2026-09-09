package control

import (
	"context"
	"errors"
	"sort"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
)

// WriterRecord retains an untrusted reply and its validated approval target.
// It is proposal provenance, not a provider execution or effect receipt.
type WriterRecord struct {
	Invocation runtime.Invocation `json:"invocation"`
	Result     runtime.Result     `json:"result"`
	Prepared   PreparedFiles      `json:"prepared"`
}

func replayWriterProposal(s *Snapshot, e journal.Event, seen map[string]bool) error {
	var record WriterRecord
	if err := canonical.Decode(e.Payload, &record); err != nil {
		return err
	}
	base, err := writerInvocation(*s)
	if err != nil {
		return err
	}
	i, err := resolveScheduledRecordedInvocation(*s, base, record.Invocation)
	if err != nil || i != record.Invocation {
		return errors.New("writer proposal invocation mismatch")
	}
	if err := runtime.ValidateResult(i, record.Result, i.Profile.Runtime == "codex-app-server"); err != nil {
		return err
	}
	if i.Profile.Runtime == "codex-app-server" {
		if s.WriterHost == nil || s.WriterHost.Intent.Invocation != i || s.WriterHost.RuntimeReceipt == nil {
			return errors.New("Codex writer proposal requires linked runtime receipt")
		}
		hash, err := canonical.Hash("harness.writer-result.v1", record.Result)
		if err != nil {
			return err
		}
		if hash != s.WriterHost.RuntimeReceipt.ResultHash {
			return errors.New("writer proposal differs from observed runtime result")
		}
	}
	reply, err := decodeWriterProposal(s.Creation.Config.WriterContract, record.Result.Output)
	if err != nil {
		return err
	}
	id, err := s.Candidate.ID()
	if err != nil {
		return err
	}
	if reply.CandidateID != id {
		return errors.New("writer proposal candidate mismatch")
	}
	expected, err := preparedFiles(*s, record.Prepared.Proposal)
	if err != nil {
		return err
	}
	a, err := canonical.Bytes(expected)
	if err != nil {
		return err
	}
	b, err := canonical.Bytes(record.Prepared)
	if err != nil {
		return err
	}
	if string(a) != string(b) {
		return errors.New("writer proposal effect substitution")
	}
	a, err = canonicalWriterChanges(reply.Changes)
	if err != nil {
		return err
	}
	b, err = canonicalWriterChanges(record.Prepared.Proposal.Changes)
	if err != nil {
		return err
	}
	if string(a) != string(b) {
		return errors.New("writer reply changes substituted")
	}
	key := "writer-proposal:" + i.ID
	if seen[key] {
		return errors.New("duplicate writer proposal")
	}
	seen[key] = true
	s.WriterProposal = &record
	return nil
}

// canonicalWriterChanges compares the reply with the prepared effect after
// applying the same path ordering as fileeffects.Prepare. The observed reply
// remains stored byte-for-byte in WriterRecord.Result.Output; only this
// comparison uses ordered copies.
func canonicalWriterChanges(changes []fileeffects.Change) ([]byte, error) {
	ordered := append([]fileeffects.Change(nil), changes...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	return canonical.Bytes(ordered)
}

// RecordWriterProposal prepares and durably records the exact untrusted reply.
// Append revalidates current plan/candidate after leased preparation; stale state
// cannot be relabeled as a proposal for a new candidate. No files are changed.
func RecordWriterProposal(ctx context.Context, path string, invocation runtime.Invocation, result runtime.Result) (WriterRecord, error) {
	p, err := PrepareWriterFiles(ctx, path, invocation, result)
	if err != nil {
		return WriterRecord{}, err
	}
	record := WriterRecord{invocation, result, p}
	if err := Append(path, "writer.proposed", record); err != nil {
		return WriterRecord{}, err
	}
	return record, nil
}
