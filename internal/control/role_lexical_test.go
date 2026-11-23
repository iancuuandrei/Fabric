package control

import (
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/fileeffects"
)

func TestRoleLexicalFallsBackToBaseForReviewAfterConfirmedCandidateChange(t *testing.T) {
	s := staleOverlayReviewSnapshot(t)
	currentID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	previousID, err := s.WriterProposal.Prepared.Proposal.Before.ID()
	if err != nil {
		t.Fatal(err)
	}
	if currentID == previousID || s.RILexicalOverlay.Intent.Prepared.Plan.CandidateID != previousID {
		t.Fatal("fixture does not bind a stale overlay to the confirmed prior candidate")
	}
	lex, err := roleLexical(s)
	if err != nil {
		t.Fatal(err)
	}
	if lex == nil || lex.Scope != "base_commit" || lex.OverlayID != "" || !strings.Contains(lex.Instruction, "reviewer lexical context uses the base index only") {
		t.Fatalf("review did not receive truthful base-only lexical scope: %+v", lex)
	}
}

func TestRoleLexicalRejectsUnsafeStaleOverlayFallback(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{
			name:   "unresolved overlay",
			mutate: func(s *Snapshot) { s.RILexicalOverlay.Outcome = "UNKNOWN" },
		},
		{
			name:   "mismatched base",
			mutate: func(s *Snapshot) { s.RILexicalOverlay.Intent.Prepared.Plan.BaseID = strings.Repeat("8", 64) },
		},
		{
			name:   "tampered candidate",
			mutate: func(s *Snapshot) { s.Candidate.FilesHash = strings.Repeat("9", 64) },
		},
		{
			name:   "overlay not bound to file effect before-state",
			mutate: func(s *Snapshot) { s.RILexicalOverlay.Intent.Prepared.Plan.CandidateID = strings.Repeat("7", 64) },
		},
		{
			name:   "not reviewing",
			mutate: func(s *Snapshot) { s.State = "IMPLEMENTING" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := staleOverlayReviewSnapshot(t)
			tt.mutate(&s)
			if _, err := roleLexical(s); err == nil {
				t.Fatal("unsafe stale overlay fallback was accepted")
			}
		})
	}
}

func staleOverlayReviewSnapshot(t *testing.T) Snapshot {
	t.Helper()
	s, _, _, _ := taskContextLexicalFixture(t)
	before := *s.Candidate
	after := before
	after.FilesHash = strings.Repeat("6", 64)
	proposal := fileeffects.Proposal{Version: 1, Nonce: "confirmed-review-effect", Before: before, After: after}
	prepared := PreparedFiles{Proposal: proposal}
	s.State = "REVIEWING"
	s.FileOutcome = "CONFIRMED"
	s.Candidate = &after
	s.FileIntent = &FileIntent{Prepared: prepared}
	s.WriterProposal = &WriterRecord{Prepared: prepared}
	return s
}
