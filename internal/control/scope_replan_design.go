package control

import (
	"context"
	"errors"
	"strings"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

const scopeReplanQuestionPrefix = "Review the candidate-bound scope request "

// A newly completed design must not relabel its already-receipted writer.
// Remove only the exact design question derived from that predecessor state;
// older designs and unrelated exploration remain part of the writer binding.
func scopeReplanWriterSnapshot(s Snapshot) Snapshot {
	if s.Creation.Execution == nil || s.Creation.Execution.ScopeReplanDesignVersion != 1 {
		return s
	}
	for index := len(s.Explorations) - 1; index >= 0; index-- {
		if !strings.HasPrefix(s.Explorations[index].Question, scopeReplanQuestionPrefix) {
			continue
		}
		prior := s
		prior.Explorations = append([]ExplorerRecord(nil), s.Explorations[:index]...)
		prior.Explorations = append(prior.Explorations, s.Explorations[index+1:]...)
		question, err := scopeReplanDesignQuestion(prior)
		if err == nil && question == s.Explorations[index].Question {
			return prior
		}
	}
	return s
}

// ScopeReplanDesignContext is read-only engineering evidence. The model's
// proposed ownership is still checked against the immutable scope ceiling.
type ScopeReplanDesignContext struct {
	Version              int                          `json:"version"`
	Request              ScopeReplanRecord            `json:"request"`
	Graph                engineeringplan.Graph        `json:"existing_graph"`
	CompletedEvidence    map[string]GraphTaskEvidence `json:"completed_evidence"`
	RejectedWriterResult runtime.Result               `json:"rejected_writer_result"`
	Verification         *VerificationState           `json:"verification,omitempty"`
	Review               *ReviewRecord                `json:"review,omitempty"`
}

// ScopeReplanCohortDesignContext carries a complete settled writer cohort as
// durable, read-only input to a graph-scope design turn.
type ScopeReplanCohortDesignContext struct {
	Version           int                          `json:"version"`
	Request           ScopeReplanRequest           `json:"request"`
	Graph             engineeringplan.Graph        `json:"existing_graph"`
	CompletedEvidence map[string]GraphTaskEvidence `json:"completed_evidence"`
}

func scopeReplanDesignQuestion(s Snapshot) (string, error) {
	record, err := buildScopeReplanRecordBase(s)
	if err != nil {
		return "", err
	}
	id, err := canonical.Hash("harness.scope-replan-design-request.v1", record)
	if err != nil {
		return "", err
	}
	return scopeReplanQuestionPrefix + id + ". Use the scope_replan context, existing completed evidence and available engineering intelligence. Return the exact requested relative paths only if the explicit ownership refinement is justified; otherwise return no paths and explain the rejection. Do not modify files or expand the immutable scope.", nil
}

func scopeReplanCohortDesignQuestion(s Snapshot) (string, error) {
	if s.Creation.Execution == nil || s.Creation.Execution.ScopeReplanVersion != 2 || s.Creation.Execution.ScopeReplanDesignVersion != 2 || len(s.ScopeReplanRequests) != len(s.ScopeReplans)+1 {
		return "", errors.New("cohort scope request is not durably admitted")
	}
	request := s.ScopeReplanRequests[len(s.ScopeReplanRequests)-1]
	id, err := canonical.Hash("harness.scope-replan-cohort-design-request.v2", request)
	if err != nil {
		return "", err
	}
	return scopeReplanQuestionPrefix + id + ". Review this complete settled cohort and its exact candidate-bound changes. Return the exact sorted requested relative paths only if the ownership refinement is justified; otherwise return no paths. This design is advisory and grants no write authority. Do not execute tools that modify files.", nil
}

func scopeReplanDesignContextForQuestion(s Snapshot, question string) (*ScopeReplanDesignContext, error) {
	if s.Creation.Execution == nil || s.Creation.Execution.ScopeReplanDesignVersion != 1 {
		return nil, nil
	}
	expected, err := scopeReplanDesignQuestion(s)
	// Ordinary research is unaffected when no scope request is available.
	if err != nil || question != expected {
		return nil, nil
	}
	record, err := buildScopeReplanRecordBase(s)
	if err != nil {
		return nil, err
	}
	return &ScopeReplanDesignContext{Version: 1, Request: record, Graph: s.Graph.Graph, CompletedEvidence: s.Graph.Evidence, RejectedWriterResult: s.WriterProposal.Result, Verification: s.Verification, Review: s.Review}, nil
}

func scopeReplanCohortDesignContextForQuestion(s Snapshot, question string) (*ScopeReplanCohortDesignContext, error) {
	if s.Creation.Execution == nil || s.Creation.Execution.ScopeReplanVersion != 2 || s.Creation.Execution.ScopeReplanDesignVersion != 2 {
		return nil, nil
	}
	expected, err := scopeReplanCohortDesignQuestion(s)
	if err != nil || question != expected {
		return nil, nil
	}
	request := s.ScopeReplanRequests[len(s.ScopeReplanRequests)-1]
	if s.Graph == nil || s.Candidate == nil {
		return nil, errors.New("cohort scope design request no longer matches the current candidate")
	}
	candidateID, candidateErr := s.Candidate.ID()
	requestID, requestErr := scopeReplanRequestID(request)
	if candidateErr != nil || requestErr != nil || request.RequestID != requestID || request.GraphDigest != s.Graph.Digest || request.CandidateID != candidateID {
		return nil, errors.New("cohort scope design request no longer matches the current candidate")
	}
	return &ScopeReplanCohortDesignContext{Version: 2, Request: request, Graph: s.Graph.Graph, CompletedEvidence: s.Graph.Evidence}, nil
}

func bindScopeReplanDesign(s Snapshot, record *ScopeReplanRecord) error {
	question, err := scopeReplanDesignQuestion(s)
	if err != nil {
		return err
	}
	var found *ExplorerRecord
	for index := range s.Explorations {
		if s.Explorations[index].Question == question {
			if found != nil {
				return ErrAutonomousUnsafe
			}
			found = &s.Explorations[index]
		}
	}
	if found == nil {
		return errors.New("scope replan requires its completed read-only design")
	}
	// RecordExploration validates invocation/receipt/candidate at admission;
	// replay binds this exact durable record rather than calling a model again.
	if err := runtime.ValidateResult(found.Invocation, found.Result, true); err != nil {
		return errors.Join(ErrAutonomousUnsafe, err)
	}
	var decision Exploration
	if err := canonical.Decode([]byte(found.Result.Output), &decision); err != nil {
		return err
	}
	if decision.CandidateID != record.CandidateID || !stringListsEqual(decision.Paths, record.RequestedPaths) {
		return errors.New("scope design did not approve the exact candidate-bound ownership request")
	}
	hash, err := canonical.Hash("harness.explorer-result.v1", found.Result)
	if err != nil {
		return err
	}
	record.DesignInvocationID, record.DesignResultHash = found.Invocation.ID, hash
	return nil
}

func bindScopeReplanCohortDesign(s Snapshot, record *ScopeReplanRecord, request ScopeReplanRequest) error {
	question, err := scopeReplanCohortDesignQuestion(s)
	if err != nil {
		return err
	}
	var found *ExplorerRecord
	for index := range s.Explorations {
		if s.Explorations[index].Question == question {
			if found != nil {
				return ErrAutonomousUnsafe
			}
			found = &s.Explorations[index]
		}
	}
	if found == nil {
		return errors.New("cohort scope replan requires its completed read-only design")
	}
	if err := runtime.ValidateResult(found.Invocation, found.Result, true); err != nil {
		return errors.Join(ErrAutonomousUnsafe, err)
	}
	var decision Exploration
	if err := canonical.Decode([]byte(found.Result.Output), &decision); err != nil {
		return err
	}
	if decision.CandidateID != request.CandidateID || !stringListsEqual(decision.Paths, request.RequestedPaths) {
		return errors.New("cohort scope design did not approve the exact candidate-bound ownership request")
	}
	hash, err := canonical.Hash("harness.explorer-result.v1", found.Result)
	if err != nil {
		return err
	}
	record.DesignInvocationID, record.DesignResultHash = found.Invocation.ID, hash
	return nil
}

func ensureScopeReplanDesign(ctx context.Context, path string, s Snapshot) error {
	question, err := scopeReplanDesignQuestion(s)
	if err != nil {
		return err
	}
	for _, record := range s.Explorations {
		if record.Question == question {
			return nil
		}
	}
	// RunExplorer uses the existing read-only runtime and effect reconciliation.
	// A pending or UNKNOWN predecessor cannot cause a new model request.
	if err := autonomousDispatchBlocked(s); err != nil {
		return err
	}
	if autonomousAuxiliaryEffectUnknown(s) {
		return ErrAutonomousReconciliation
	}
	_, err = RunExplorer(ctx, path, question)
	return err
}

func ensureScopeReplanCohortDesign(ctx context.Context, path string, s Snapshot) error {
	question, err := scopeReplanCohortDesignQuestion(s)
	if err != nil {
		return err
	}
	for _, record := range s.Explorations {
		if record.Question == question {
			return nil
		}
	}
	if err := autonomousDispatchBlocked(s); err != nil {
		return err
	}
	if autonomousAuxiliaryEffectUnknown(s) {
		return ErrAutonomousReconciliation
	}
	_, err = RunExplorer(ctx, path, question)
	return err
}
