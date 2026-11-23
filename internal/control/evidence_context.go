package control

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/evidencevalue"
	"github.com/iancuuandrei/fabric/internal/journal"
)

// EvidenceContextRequest binds a finite source-acquisition model and its query
// targets to one controller prefix. Queries cannot grant filesystem authority.
type EvidenceContextRequest struct {
	Model   evidencevalue.WireRequest `json:"model"`
	Queries map[string]string         `json:"queries"`
}

// EvidenceContextDecision retains the supplied model, targets and recomputable
// selection. It is derived decision evidence, not verified engineering truth.
type EvidenceContextDecision struct {
	ID      string                   `json:"id"`
	Request EvidenceContextRequest   `json:"request"`
	Report  evidencevalue.WireReport `json:"report"`
	// EvidencePolicyHash tags the single automatic policy acquisition. Empty
	// preserves ordinary operator evidence-acquire records.
	EvidencePolicyHash string `json:"evidence_policy_hash,omitempty"`
}

// EvidenceContextResult contains a decision and, only after ordinary context
// admission, the selected bounded source evidence. No model is dispatched.
type EvidenceContextResult struct {
	Decision EvidenceContextDecision `json:"decision"`
	Context  *TaskContextRecord      `json:"context,omitempty"`
}

// EvidenceBinding derives the exact identities needed by finite evidence models.
func EvidenceBinding(s Snapshot, head string) (evidencevalue.Binding, error) {
	source, err := s.Creation.Repository.ID()
	if err != nil {
		return evidencevalue.Binding{}, err
	}
	candidate := ""
	if s.Candidate != nil {
		candidate, err = s.Candidate.ID()
		if err != nil {
			return evidencevalue.Binding{}, err
		}
	}
	return evidencevalue.Binding{RunID: s.RunID, SourceID: source, CandidateID: candidate, JournalHead: head}, nil
}

// ApplyEvidenceStop suppresses even advisory selection at controller stop gates.
// UNKNOWN takes precedence over an otherwise accepted checkpoint.
func ApplyEvidenceStop(s Snapshot, report *evidencevalue.Report) {
	if reason := evidenceControllerStopReason(s); reason != "" {
		report.Selected = ""
		report.StopReason = reason
	}
}

func evidenceControllerStopReason(s Snapshot) string {
	if stop := ClassifyAutonomousFailure(s, errors.New("evidence advisory inspection")); stop.Disposition() == GateReconcile {
		return stop.PublicReason()
	}
	if s.State == "READY" {
		return "accepted_checkpoint_requires_no_additional_research"
	}
	if lifecycleStatus(s) != LifecycleActive {
		return "lifecycle_requires_attention"
	}
	return ""
}

func evidenceContextDecision(s Snapshot, head string, request EvidenceContextRequest) (EvidenceContextDecision, error) {
	var decision EvidenceContextDecision
	raw, err := canonical.Bytes(request)
	if err != nil || len(raw) > evidencevalue.MaxBytes {
		return decision, errors.New("evidence acquisition request exceeds encoding bounds")
	}
	model, err := request.Model.Decode()
	if err != nil {
		return decision, err
	}
	binding, err := EvidenceBinding(s, head)
	if err != nil {
		return decision, err
	}
	if model.Binding != binding {
		return decision, errors.New("evidence acquisition binding is stale or substituted")
	}
	if len(request.Queries) != len(model.Actions) {
		return decision, errors.New("evidence acquisition targets differ from action frontier")
	}
	for _, action := range model.Actions {
		question, ok := request.Queries[action.ID]
		if action.Kind != "source_read" || !ok || strings.TrimSpace(question) == "" || len(question) > taskContextQueryCap || !utf8.ValidString(question) {
			return decision, errors.New("evidence acquisition requires bounded source_read queries")
		}
	}
	report, err := evidencevalue.Evaluate(model)
	if err != nil {
		return decision, err
	}
	ApplyEvidenceStop(s, &report)
	decision.Request, decision.Report = request, report.Wire()
	decision.Report.RequestHash, err = canonical.Hash("harness.evidence-value-request.v1", request.Model)
	if err != nil {
		return decision, err
	}
	decision.ID, err = evidenceContextDecisionIDFor(decision.Request, decision.Report, "")
	if err != nil {
		return decision, err
	}
	return decision, err
}

// evidenceContextDecisionIDFor binds the optional automatic-policy tag into
// the decision identity. The tag field uses omitempty so empty-tag decisions
// keep the exact historical absent-policy/manual identity.
func evidenceContextDecisionIDFor(request EvidenceContextRequest, report evidencevalue.WireReport, policyHash string) (string, error) {
	return canonical.Hash("harness.evidence-context-decision.v1", struct {
		Request EvidenceContextRequest   `json:"request"`
		Report  evidencevalue.WireReport `json:"report"`
		Policy  string                   `json:"evidence_policy_hash,omitempty"`
	}{request, report, policyHash})
}

// AcquireEvidenceContext records a finite decision before optional source
// acquisition through existing bounded explorer context admission. Stop gates
// perform no acquisition or journal writes. Invalid/stale input is rejected;
// acquisition failure retains its decision and never authorizes provider retry.
// A request matching the frozen automatic policy template is tagged with its
// deterministic hash and consumes the single automatic opportunity.
func AcquireEvidenceContext(ctx context.Context, path string, request EvidenceContextRequest) (EvidenceContextResult, error) {
	var result EvidenceContextResult
	if ctx == nil {
		return result, errors.New("evidence acquisition context required")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	s, head, err := InspectWithHead(path)
	if err != nil {
		return result, err
	}
	decision, err := evidenceContextDecision(s, head, request)
	if err != nil {
		return result, err
	}
	if s.Creation.Execution != nil && s.Creation.Execution.EvidencePolicy != nil {
		template := *s.Creation.Execution.EvidencePolicy
		if evidencePolicyStrippedEqual(template, request) {
			expectedHash, hashErr := EvidenceAutoPolicyHash(template)
			if hashErr != nil {
				return result, hashErr
			}
			if hasEvidencePolicyDecision(s, expectedHash) {
				return result, errors.New("evidence policy acquisition already recorded")
			}
			decision.EvidencePolicyHash = expectedHash
			boundID, idErr := evidenceContextDecisionIDFor(decision.Request, decision.Report, expectedHash)
			if idErr != nil {
				return result, idErr
			}
			decision.ID = boundID
		}
	}
	result.Decision = decision
	if evidenceControllerStopReason(s) != "" {
		return result, nil
	}
	if !taskContextEnabled(s) || s.Candidate == nil || s.Workspace == nil {
		return result, errors.New("evidence acquisition requires bounded context and a resolved candidate")
	}
	if err := Append(path, "evidence.context-decided", decision); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, errors.Join(errors.New("bounded evidence acquisition failed; decision "+decision.ID+" retained; inspect run before preparing a fresh snapshot request"), err)
	}
	if decision.Report.Selected == "" {
		return result, nil
	}
	acquired, err := admitTaskContextBound(ctx, path, "explorer", request.Queries[decision.Report.Selected], "", nil, &decision)
	if err != nil {
		return result, errors.Join(errors.New("bounded evidence acquisition failed; decision "+decision.ID+" retained; inspect run before preparing a fresh snapshot request"), err)
	}
	result.Context = &acquired
	return result, nil
}

func replayEvidenceContextDecision(s *Snapshot, event journal.Event) error {
	var decision EvidenceContextDecision
	if err := canonical.Decode(event.Payload, &decision); err != nil {
		return err
	}
	if !taskContextEnabled(*s) || s.Candidate == nil || s.Workspace == nil || s.State == "READY" || lifecycleStatus(*s) != LifecycleActive {
		return errors.New("evidence decision requires active bounded candidate context")
	}
	matchesTemplate := false
	expectedHash := ""
	if s.Creation.Execution != nil && s.Creation.Execution.EvidencePolicy != nil {
		template := *s.Creation.Execution.EvidencePolicy
		if evidencePolicyStrippedEqual(template, decision.Request) {
			matchesTemplate = true
			hash, hashErr := EvidenceAutoPolicyHash(template)
			if hashErr != nil {
				return hashErr
			}
			expectedHash = hash
		}
	}
	if matchesTemplate {
		if decision.EvidencePolicyHash != expectedHash {
			return errors.New("evidence policy decision tag removal or substitution")
		}
		if hasEvidencePolicyDecision(*s, expectedHash) {
			return errors.New("duplicate evidence policy decision")
		}
	} else if decision.EvidencePolicyHash != "" {
		if s.Creation.Execution == nil || s.Creation.Execution.EvidencePolicy == nil {
			return errors.New("evidence policy decision without frozen policy")
		}
		return errors.New("evidence policy decision substitution")
	}
	expected, err := evidenceContextDecision(*s, event.Previous, decision.Request)
	if err != nil {
		return err
	}
	if evidenceControllerStopReason(*s) != "" {
		return errors.New("evidence decision requires controller progress before acquisition")
	}
	if matchesTemplate {
		expected.EvidencePolicyHash = expectedHash
		boundID, idErr := evidenceContextDecisionIDFor(expected.Request, expected.Report, expectedHash)
		if idErr != nil {
			return idErr
		}
		expected.ID = boundID
	}
	want, err := canonical.Bytes(expected)
	if err != nil {
		return err
	}
	got, err := canonical.Bytes(decision)
	if err != nil || !bytes.Equal(got, want) {
		return errors.New("evidence acquisition decision substitution")
	}
	s.EvidenceDecisions = append(s.EvidenceDecisions, decision)
	return nil
}

func evidenceContextDecisionID(decision *EvidenceContextDecision) string {
	if decision == nil {
		return ""
	}
	return decision.ID
}

func findEvidenceContextDecision(s Snapshot, id string) *EvidenceContextDecision {
	for i := range s.EvidenceDecisions {
		if s.EvidenceDecisions[i].ID == id {
			return &s.EvidenceDecisions[i]
		}
	}
	return nil
}

func requireEvidenceContextBinding(s Snapshot, role, question string, decision *EvidenceContextDecision) error {
	if decision == nil {
		return nil
	}
	if evidenceControllerStopReason(s) != "" {
		return errors.New("evidence acquisition requires controller progress before source admission")
	}
	admitted := findEvidenceContextDecision(s, decision.ID)
	if admitted == nil || admitted.Report.Selected == "" || role != "explorer" || admitted.Request.Queries[admitted.Report.Selected] != question {
		return errors.New("evidence acquisition requires exact admitted selected query")
	}
	actual, err := EvidenceBinding(s, admitted.Request.Model.Binding.JournalHead)
	if err != nil || actual != admitted.Request.Model.Binding {
		return errors.New("evidence acquisition source or candidate changed after decision")
	}
	return nil
}
