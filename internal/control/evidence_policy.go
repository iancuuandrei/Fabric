package control

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/evidencevalue"
)

// EvidenceAutoPolicy is an opt-in finite EVC source acquisition template frozen
// at run.created. The model binding stays empty in the template and is filled
// from the actual run at decision time. Estimates remain caller-supplied
// unvalidated advisory values; the controller never invents resource costs.
type EvidenceAutoPolicy struct {
	Version int                       `json:"version"`
	Model   evidencevalue.WireRequest `json:"model"`
	Queries map[string]string         `json:"queries"`
}

// ValidateEvidenceAutoPolicyTemplate rejects malformed templates before run
// creation or provider dispatch. It checks version, empty binding, wire bounds,
// finite estimates and the exact source_read frontier.
func ValidateEvidenceAutoPolicyTemplate(policy EvidenceAutoPolicy) error {
	if policy.Version != 1 {
		return errors.New("evidence policy version must be 1")
	}
	if policy.Model.Version != 1 {
		return errors.New("evidence policy model version must be 1")
	}
	if policy.Model.Binding != (evidencevalue.Binding{}) {
		return errors.New("evidence policy template must not supply run, source, candidate or journal head binding")
	}
	if policy.Queries == nil {
		return errors.New("evidence policy queries missing")
	}
	model, err := policy.Model.Decode()
	if err != nil {
		return err
	}
	if _, err := evidencevalue.Evaluate(model); err != nil {
		return err
	}
	if len(policy.Queries) != len(model.Actions) {
		return errors.New("evidence policy targets differ from action frontier")
	}
	for _, action := range model.Actions {
		question, ok := policy.Queries[action.ID]
		if action.Kind != "source_read" || !ok || strings.TrimSpace(question) == "" || len(question) > taskContextQueryCap || !utf8.ValidString(question) {
			return errors.New("evidence policy requires bounded source_read queries")
		}
	}
	raw, err := canonical.Bytes(policy)
	if err != nil || len(raw) > evidencevalue.MaxBytes {
		return errors.New("evidence policy exceeds encoding bounds")
	}
	return nil
}

// EvidenceAutoPolicyHash binds the exact immutable template. It is identity
// evidence, not execution authority.
func EvidenceAutoPolicyHash(policy EvidenceAutoPolicy) (string, error) {
	return canonical.Hash("harness.evidence-auto-policy.v1", policy)
}

func evidencePolicyExpectedHash(s Snapshot) (string, error) {
	if s.Creation.Execution == nil || s.Creation.Execution.EvidencePolicy == nil {
		return "", errors.New("evidence policy not enabled for this run")
	}
	return EvidenceAutoPolicyHash(*s.Creation.Execution.EvidencePolicy)
}

func evidencePolicyStrippedEqual(policy EvidenceAutoPolicy, request EvidenceContextRequest) bool {
	if policy.Version != 1 || request.Model.Version != policy.Model.Version {
		return false
	}
	wantRes, err := canonical.Bytes(policy.Model.Resources)
	if err != nil {
		return false
	}
	gotRes, err := canonical.Bytes(request.Model.Resources)
	if err != nil || !bytes.Equal(wantRes, gotRes) {
		return false
	}
	wantActions, err := canonical.Bytes(policy.Model.Actions)
	if err != nil {
		return false
	}
	gotActions, err := canonical.Bytes(request.Model.Actions)
	if err != nil || !bytes.Equal(wantActions, gotActions) {
		return false
	}
	wantQueries, err := canonical.Bytes(policy.Queries)
	if err != nil {
		return false
	}
	gotQueries, err := canonical.Bytes(request.Queries)
	if err != nil || !bytes.Equal(wantQueries, gotQueries) {
		return false
	}
	return true
}

func hasEvidencePolicyDecision(s Snapshot, expectedHash string) bool {
	if expectedHash == "" {
		return false
	}
	for i := range s.EvidenceDecisions {
		if s.EvidenceDecisions[i].EvidencePolicyHash == expectedHash {
			return true
		}
	}
	return false
}

// evidencePolicyRequest fills the frozen template binding from the live run.
func evidencePolicyRequest(s Snapshot, head string) (EvidenceContextRequest, string, error) {
	var request EvidenceContextRequest
	if s.Creation.Execution == nil || s.Creation.Execution.EvidencePolicy == nil {
		return request, "", errors.New("evidence policy not enabled for this run")
	}
	template := *s.Creation.Execution.EvidencePolicy
	binding, err := EvidenceBinding(s, head)
	if err != nil {
		return request, "", err
	}
	request.Model = template.Model
	request.Model.Binding = binding
	request.Queries = make(map[string]string, len(template.Queries))
	for k, v := range template.Queries {
		request.Queries[k] = v
	}
	hash, err := EvidenceAutoPolicyHash(template)
	if err != nil {
		return request, "", err
	}
	return request, hash, nil
}

// evidencePolicyWriterStarted reports whether initial serial writer work has
// begun. Candidate advancement, new writer queries or repair must not reset
// the single automatic opportunity.
func evidencePolicyWriterStarted(s Snapshot) bool {
	if s.WriterProposal != nil || s.FileIntent != nil || s.Verification != nil || s.ReviewHost != nil || s.Review != nil {
		return true
	}
	if len(s.GraphWriterHosts) != 0 || len(s.GraphWriterResults) != 0 || s.GraphWriterBatch != nil {
		return true
	}
	for _, rec := range s.TaskContexts {
		if rec.Role == "writer" || rec.Role == "fixer" {
			return true
		}
	}
	if s.Graph != nil {
		for _, t := range s.Graph.Graph.Tasks {
			if len(t.Attempts) != 0 || t.Completed {
				if t.Kind == "implementation" {
					return true
				}
			}
		}
		for _, ev := range s.Graph.Evidence {
			if ev.WriterInvocationID != "" || ev.CandidateID != "" {
				return true
			}
		}
	}
	return false
}

// maybeAcquireEvidencePolicy performs the single automatic acquisition before
// the initial serial writer. It returns the refreshed snapshot. No-positive
// or unavailable-cost outcomes still proceed to ordinary writer context.
// Stop gates perform no reads or journal writes. A hard acquisition failure
// after the decision append always propagates its retained decision and safe
// diagnostic; the next resume observes the retained decision and degrades to
// ordinary required role context without retry.
func maybeAcquireEvidencePolicy(ctx context.Context, path string, s Snapshot) (Snapshot, error) {
	if s.Creation.Execution == nil || s.Creation.Execution.EvidencePolicy == nil {
		return s, nil
	}
	if s.Creation.Execution.ParallelImplementationVersion != 0 || s.Creation.Execution.IsolatedImplementationVersion != 0 {
		return s, errors.New("evidence policy incompatible with parallel or isolated writers")
	}
	expectedHash, err := evidencePolicyExpectedHash(s)
	if err != nil {
		return s, err
	}
	if hasEvidencePolicyDecision(s, expectedHash) {
		return s, nil
	}
	if evidencePolicyWriterStarted(s) {
		return s, nil
	}
	if evidenceControllerStopReason(s) != "" {
		return s, nil
	}
	if !taskContextEnabled(s) || s.Candidate == nil || s.Workspace == nil {
		return s, errors.New("evidence policy requires bounded context and a resolved candidate")
	}
	fresh, head, err := InspectWithHead(path)
	if err != nil {
		return s, err
	}
	if hasEvidencePolicyDecision(fresh, expectedHash) || evidencePolicyWriterStarted(fresh) || evidenceControllerStopReason(fresh) != "" {
		return fresh, nil
	}
	request, _, err := evidencePolicyRequest(fresh, head)
	if err != nil {
		return fresh, err
	}
	result, acquireErr := AcquireEvidenceContext(ctx, path, request)
	latest, inspectErr := Inspect(path)
	if inspectErr != nil {
		if acquireErr != nil {
			return latest, errors.Join(acquireErr, inspectErr)
		}
		return latest, inspectErr
	}
	if acquireErr != nil {
		_ = result
		return latest, acquireErr
	}
	return latest, nil
}
