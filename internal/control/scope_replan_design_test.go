package control

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestScopeReplanDesignPreservesContextAndRequiresExactDecision(t *testing.T) {
	s := scopeReplanFixture(t)
	s.Creation.Execution.ScopeReplanDesignVersion = 1
	s.Creation.Config.Explorer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "design-fixture", Effort: "none", Role: "explorer"}
	question, err := scopeReplanDesignQuestion(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(question) > 4096 {
		t.Fatal("design question exceeded existing admission bound")
	}
	invocation, err := explorerInvocation(s, question)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		ScopeReplan *ScopeReplanDesignContext `json:"scope_replan"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.ScopeReplan == nil || envelope.ScopeReplan.CompletedEvidence["research"].Outcome != "completed" || envelope.ScopeReplan.RejectedWriterResult.InvocationID != s.WriterProposal.Result.InvocationID || envelope.ScopeReplan.Request.CandidateID == "" || len(envelope.ScopeReplan.Graph.Tasks) != len(s.Graph.Graph.Tasks) {
		t.Fatal("design omitted engineering evidence")
	}
	if _, err := buildScopeReplanRecord(s); err == nil {
		t.Fatal("scope changed before model decision")
	}
	output, err := canonical.Bytes(Exploration{CandidateID: envelope.ScopeReplan.Request.CandidateID, Summary: "approve exact requested ownership", Paths: envelope.ScopeReplan.Request.RequestedPaths})
	if err != nil {
		t.Fatal(err)
	}
	model := invocation.Profile.Model
	result := runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, Output: string(output)}
	s.Explorations = []ExplorerRecord{{Question: question, Invocation: invocation, Result: result}}
	record, err := buildScopeReplanRecord(s)
	if err != nil || record.DesignInvocationID != invocation.ID || record.DesignResultHash == "" {
		t.Fatal("design decision not bound", err)
	}
	wrong := s
	wrong.Explorations = append([]ExplorerRecord(nil), s.Explorations...)
	wrong.Explorations[0].Result.Output = strings.Replace(string(output), "pkg/new.go", "private/escape.go", 1)
	if _, err := buildScopeReplanRecord(wrong); err == nil {
		t.Fatal("design widened ownership beyond exact request")
	}
	ordinary, err := scopeReplanDesignContextForQuestion(s, "ordinary research")
	if err != nil || ordinary != nil {
		t.Fatal("scope context leaked into unrelated research", err)
	}
}

func TestCohortScopeReplanDesignCarriesOnlyDurableExactRequest(t *testing.T) {
	s := scopeReplanFixture(t)
	s.Creation.Execution.MaxParallel = 2
	s.Creation.Execution.Context = taskContextBoundedV1
	s.Creation.Execution.ParallelImplementationVersion = 1
	s.Creation.Execution.ScopeReplanVersion = 2
	s.Creation.Execution.ScopeReplanDesignVersion = 2
	if err := s.Creation.Execution.Validate(); err != nil {
		t.Fatal(err)
	}
	// The scope context contract itself is tested here; task-context admission
	// has separate journal-backed coverage and is omitted from this pure recipe test.
	s.Creation.Execution.Context = ""
	candidateID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	request := ScopeReplanRequest{Version: 1, Sequence: 1, RunID: s.RunID, PlanID: s.Graph.PlanID, GraphDigest: s.Graph.Digest, Revision: s.Graph.Revision, CandidateID: candidateID, RequestedPaths: []string{"pkg/new.go"}}
	request.RequestID, err = scopeReplanRequestID(request)
	if err != nil {
		t.Fatal(err)
	}
	s.ScopeReplanRequests = []ScopeReplanRequest{request}
	question, err := scopeReplanCohortDesignQuestion(s)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := explorerInvocation(s, question)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Legacy *ScopeReplanDesignContext       `json:"scope_replan"`
		Cohort *ScopeReplanCohortDesignContext `json:"cohort_scope_replan"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Legacy != nil || envelope.Cohort == nil || envelope.Cohort.Version != 2 || envelope.Cohort.Request.RequestID != request.RequestID || envelope.Cohort.Request.CandidateID != candidateID {
		t.Fatal("cohort design did not receive the exact durable request")
	}
	ordinary, err := scopeReplanCohortDesignContextForQuestion(s, "ordinary research")
	if err != nil || ordinary != nil {
		t.Fatal("cohort scope request leaked into unrelated research", err)
	}
}
