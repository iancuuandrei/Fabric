package control

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// This is an already-replayed projection fixture for the pure aggregate seam.
// It does not qualify scheduler settlement, model design or actual file effects.
func scopeAggregateProjectionFixture(t *testing.T) Snapshot {
	t.Helper()
	s := scopeReplanFixture(t)
	content := base64.StdEncoding.EncodeToString([]byte("package pkg\n"))
	s.WriterProposal.Prepared.Proposal.Changes = []fileeffects.Change{{Path: "pkg/new.go", ContentBase64: &content}}
	revision, err := buildScopeReplanRecord(s)
	if err != nil {
		t.Fatal(err)
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	sourceID, err := s.Creation.Repository.ID()
	if err != nil {
		t.Fatal(err)
	}
	request := ScopeReplanRequest{
		Version: 1, Sequence: 1, RunID: s.RunID, PlanID: s.PlanID,
		SourceID: sourceID, CandidateID: candidateID, GraphDigest: s.Graph.Digest,
		Revision: 1, GraphHash: revision.GraphHash, NextRevision: revision.Revision,
		Graph:   revision.Graph,
		Updates: []ScopeReplanTaskUpdate{{TaskID: revision.TaskID, NextTaskID: revision.NextTaskID, RequestedPaths: revision.RequestedPaths}},
		Cohort: SettledGraphWriterCohort{Members: []SettledGraphWriterMember{{
			TaskID: revision.TaskID, Invocation: s.WriterProposal.Invocation,
			RuntimeReceipt: *s.WriterHost.RuntimeReceipt, ResultHash: s.WriterHost.RuntimeReceipt.ResultHash,
			Changes: s.WriterProposal.Prepared.Proposal.Changes,
		}}},
	}
	request.RequestID, err = scopeReplanRequestID(request)
	if err != nil {
		t.Fatal(err)
	}
	s.Creation.Execution.ScopeReplanVersion = 2
	s.Creation.Execution.ScopeReplanDesignVersion = 2
	s.Creation.Execution.ParallelImplementationVersion = 1
	s.Creation.Execution.Context = taskContextBoundedV1
	if err := s.Creation.Execution.Validate(); err != nil {
		t.Fatal(err)
	}
	revision.Version = 2
	revision.CohortRequestID = request.RequestID
	s.ScopeReplanRequests = []ScopeReplanRequest{request}
	s.ScopeReplans = []ScopeReplanRecord{revision}
	s.Graph.Graph = revision.Graph
	s.Graph.Digest = revision.GraphHash
	s.Graph.Revision = revision.Revision
	return s
}

func TestScopeAggregateUsesHistoricalInvocationAndNewParentEffect(t *testing.T) {
	s := scopeAggregateProjectionFixture(t)
	batch, err := buildScopeReplannedGraphWriterBatch(s, []worktree.FileState{})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Version != 3 || batch.ScopeReplanRequestID != s.ScopeReplanRequests[0].RequestID || len(batch.Members) != 1 || batch.Members[0].TaskID != s.ScopeReplans[0].NextTaskID || batch.Members[0].InvocationID != s.WriterProposal.Invocation.ID {
		t.Fatal("aggregate lost explicit replan or original runtime provenance")
	}
	if batch.Prepared.Intent.Kind != "filesystem" || batch.Prepared.Proposal.Before != *s.Candidate || batch.IsolationPreparationID != "" || !strings.HasPrefix(batch.Prepared.Proposal.Nonce, "scope-writers-") {
		t.Fatal("aggregate did not prepare a separate parent file effect")
	}
	if len(s.GraphWriterResults) != 0 {
		t.Fatal("bridge fabricated an accepted model result")
	}
	if err := replayScopeReplannedGraphWriterBatch(&s, batch); err != nil {
		t.Fatal(err)
	}
	if changes, ok := scopeReplannedWriterChanges(s, batch.Members[0].TaskID, batch.Members[0].InvocationID); !ok || len(changes) != 1 || changes[0].Path != "pkg/new.go" {
		t.Fatal("aggregate cannot resolve retained writer evidence")
	}
	if _, ok := scopeReplannedWriterChanges(s, batch.Members[0].TaskID, strings.Repeat("f", 64)); ok {
		t.Fatal("foreign invocation claimed historical writer work")
	}
	if err := replayScopeReplannedGraphWriterBatch(&s, batch); err == nil {
		t.Fatal("duplicate aggregate admitted")
	}
}

func TestScopeAggregateRejectsCandidateRequestAndOwnershipSubstitution(t *testing.T) {
	for name, mutate := range map[string]func(*Snapshot){
		"candidate":      func(s *Snapshot) { s.Candidate.IndexHash = strings.Repeat("f", 64) },
		"request":        func(s *Snapshot) { s.ScopeReplanRequests[0].Cohort.Members[0].Changes[0].Path = "private/escape.go" },
		"revision":       func(s *Snapshot) { s.ScopeReplans[0].CohortRequestID = strings.Repeat("f", 64) },
		"pending effect": func(s *Snapshot) { s.FileOutcome = "UNKNOWN" },
		"new ownership":  func(s *Snapshot) { s.Graph.Graph.Tasks[1].WritePaths = []string{"pkg/old.go"} },
	} {
		t.Run(name, func(t *testing.T) {
			s := scopeAggregateProjectionFixture(t)
			mutate(&s)
			if _, err := buildScopeReplannedGraphWriterBatch(s, []worktree.FileState{}); err == nil {
				t.Fatal("substituted aggregate admitted")
			}
		})
	}
	s := scopeAggregateProjectionFixture(t)
	batch, err := buildScopeReplannedGraphWriterBatch(s, []worktree.FileState{})
	if err != nil {
		t.Fatal(err)
	}
	batch.Prepared.Proposal.Nonce = "foreign-parent-effect"
	if err := replayScopeReplannedGraphWriterBatch(&s, batch); err == nil {
		t.Fatal("foreign prepared effect admitted")
	}
}

func TestScopeAggregateUsesFinalCorrectedInvocationWithoutReplacingStaticProvenance(t *testing.T) {
	s := scopeAggregateProjectionFixture(t)
	request := &s.ScopeReplanRequests[0]
	member := &request.Cohort.Members[0]
	staticID := member.Invocation.ID
	corrected := member.Invocation
	corrected.ID = strings.Repeat("f", 64)
	member.ResultInvocation = &corrected
	member.RuntimeReceipt.InvocationID = corrected.ID
	var err error
	request.RequestID, err = scopeReplanRequestID(*request)
	if err != nil {
		t.Fatal(err)
	}
	s.ScopeReplans[0].CohortRequestID = request.RequestID
	batch, err := buildScopeReplannedGraphWriterBatch(s, []worktree.FileState{})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Members[0].InvocationID != corrected.ID || request.Cohort.Members[0].Invocation.ID != staticID {
		t.Fatal("correction did not preserve original scheduling provenance")
	}
	s.GraphWriterBatch = &batch
	if _, ok := scopeReplannedWriterChanges(s, batch.Members[0].TaskID, corrected.ID); !ok {
		t.Fatal("completed corrected result is not usable")
	}
	if _, ok := scopeReplannedWriterChanges(s, batch.Members[0].TaskID, staticID); ok {
		t.Fatal("rejected original output became accepted evidence")
	}
}
