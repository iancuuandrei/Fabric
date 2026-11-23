package control

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/faultlocalization"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// writerRepairIntelligence is a pure projection of existing durable records.
// It is not another admission step and never reads the workspace during replay.
func writerRepairIntelligence(s Snapshot, task *writerImplementationContext, context *TaskContextRecord) (*RepairDiagnosis, error) {
	if s.Creation.Execution == nil || s.Creation.Execution.RepairIntelligenceVersion != 1 || task == nil || s.Graph == nil {
		return nil, nil
	}
	node, ok := s.Graph.Graph.Task(task.ID)
	if !ok || node.ParentID == "" {
		return nil, nil
	}
	gate, ok := s.Graph.Evidence[node.ParentID]
	if !ok || gate.Outcome != "failed" {
		return nil, nil
	}
	r, err := DiagnoseRepair(s)
	if err != nil {
		return nil, err
	}
	// ControllerHead is an inspection cursor, not immutable invocation data.
	// Host-intent appends must never change replay-derived fixer identity.
	r.ControllerHead = ""
	r.Specifications = []RepairSpecification{}
	r.Findings = repairFindingsForGate(r.Findings, repairFailureID(gate))
	r.AnchoringStatus = "context_unavailable"
	if context != nil && context.CandidateID == r.CandidateID {
		r.AnchoringStatus = "recorded_context"
		if err := r.addRecordedRepairAnchors(s, context); err != nil {
			return nil, err
		}
	}
	spec := r.specificationForGate(node, repairFailureID(gate))
	if len(spec.FindingIDs) != 0 {
		r.Specifications = append(r.Specifications, spec)
	}
	r.attachRepairAnchors()
	if context != nil && context.RepairSpectrum != nil {
		if context.RepairSpectrum.TaskID != task.ID || context.CandidateID != r.CandidateID {
			return nil, errors.New("repair spectrum differs from writer task/candidate")
		}
		ranking, err := recordedRepairSpectrum(s, context)
		if err != nil {
			return nil, err
		}
		filtered := []faultlocalization.Block{}
		for _, block := range ranking.Blocks {
			if repairAnchorOwned(block.Path, task.WritePaths) {
				filtered = append(filtered, block)
			}
		}
		ranking.OmittedBlocks = len(ranking.Blocks) - len(filtered)
		ranking.Blocks = filtered
		r.Spectrum = ranking
	}
	return boundedWriterRepairProjection(r)
}

func repairFindingsForGate(findings []RepairFinding, gateID string) []RepairFinding {
	filtered := []RepairFinding{}
	for _, f := range findings {
		if f.GateID == gateID {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

func (r *RepairDiagnosis) addRecordedRepairAnchors(s Snapshot, context *TaskContextRecord) error {
	for _, finding := range r.Findings {
		file := recordedRepairSource(context, finding.Path)
		previous := priorRecordedRepairAnchor(s, context.SourceID, finding)
		a := resolveRepairAnchor(finding, r.CandidateID, file, previous)
		if a.Provenance == "current_candidate_observation" {
			a.Provenance = "recorded_task_context"
		}
		// The sibling task_context already contains exact admitted code.
		// Avoid duplicating it; identity binds this compact record.
		a.Code = ""
		id, err := repairAnchorID(a)
		if err != nil {
			return err
		}
		a.ID = id
		r.Anchors = append(r.Anchors, a)
	}
	return nil
}

func boundedWriterRepairProjection(r RepairDiagnosis) (*RepairDiagnosis, error) {
	if raw, err := canonical.Bytes(r); err != nil {
		return nil, err
	} else if len(raw) > 32<<10 {
		// Original verification/review evidence remains in the normal input.
		// Optional intelligence must not block a valid bounded writer payload.
		hash, err := canonical.Hash("harness.repair-prompt-projection.v1", r)
		if err != nil {
			return nil, err
		}
		return &RepairDiagnosis{Version: 1, RunID: r.RunID, CandidateID: r.CandidateID,
			Findings: []RepairFinding{}, Specifications: []RepairSpecification{},
			RepairAttempts: r.RepairAttempts, MaxRepairs: r.MaxRepairs, Authority: r.Authority,
			AnchoringStatus: "projection_budget_omitted", OmittedFindings: len(r.Findings), ProjectionHash: hash}, nil
	}
	return &r, nil
}

func recordedRepairSource(context *TaskContextRecord, path string) worktree.SourceFile {
	unavailable := worktree.SourceFile{Err: errors.New("complete recorded source unavailable")}
	if context == nil {
		return unavailable
	}
	for _, selected := range context.Manifest.Selected {
		if selected.Path != path {
			continue
		}
		if !completeRecordedRepairFile(selected) {
			return unavailable
		}
		return worktree.SourceFile{CandidateID: context.CandidateID, Path: path, SHA256: selected.Hash, Content: []byte(selected.Content), Size: int64(len(selected.Content))}
	}
	return unavailable
}

func completeRecordedRepairFile(file taskcontext.SelectedFile) bool {
	if file.Start != 0 || file.End != int64(len(file.Content)) || len(file.Content) > 32768 {
		return false
	}
	digest := sha256.Sum256([]byte(file.Content))
	return hex.EncodeToString(digest[:]) == file.Hash
}

func priorRecordedRepairAnchor(s Snapshot, sourceID string, f RepairFinding) *RepairAnchor {
	for i := len(s.TaskContexts) - 1; i >= 0; i-- {
		previous := &s.TaskContexts[i]
		if previous.SourceID != sourceID || previous.CandidateID != f.CandidateID {
			continue
		}
		file := recordedRepairSource(previous, f.Path)
		a := resolveRepairAnchor(f, f.CandidateID, file, nil)
		if !repairAnchorVerified(a.Status) || a.Code == "" {
			continue
		}
		a.Provenance = "recorded_task_context"
		return &a
	}
	return nil
}
