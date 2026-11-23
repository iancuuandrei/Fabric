package control

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/faultlocalization"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

const repairContextSpectrumMaxBytes = 128 << 10

// RepairSpectrumEvidence is optional, untrusted localization evidence admitted
// before a writer context is frozen. Empty InvocationTaskID selects the serial
// writer; otherwise it selects the exact task-bound writer query.
type RepairSpectrumEvidence struct {
	TaskID           string                     `json:"task_id"`
	InvocationTaskID string                     `json:"invocation_task_id,omitempty"`
	GateID           string                     `json:"gate_id"`
	Spectrum         faultlocalization.Spectrum `json:"spectrum"`
}

// AdmitRepairSpectrumContext uses normal candidate context admission. It cannot
// replace a frozen context, execute tests, dispatch a model or grant retry rights.
func AdmitRepairSpectrumContext(ctx context.Context, path, invocationTaskID string, spectrum faultlocalization.Spectrum) (TaskContextRecord, error) {
	s, err := Inspect(path)
	if err != nil {
		return TaskContextRecord{}, err
	}
	task, err := repairSpectrumTask(s, invocationTaskID)
	if err != nil {
		return TaskContextRecord{}, err
	}
	node, _ := s.Graph.Graph.Task(task.ID)
	question, err := writerTaskContextQuestion(s, invocationTaskID)
	if err != nil {
		return TaskContextRecord{}, err
	}
	evidence := &RepairSpectrumEvidence{TaskID: task.ID, InvocationTaskID: invocationTaskID, GateID: node.ParentID, Spectrum: spectrum}
	// Validate before candidate reads; source verification follows normal selection.
	if _, err := boundedContextSpectrum(spectrum); err != nil {
		return TaskContextRecord{}, err
	}
	return admitTaskContextWithSpectrum(ctx, path, writerTaskRole(s), question, "", evidence)
}

func writerTaskContextQuestion(s Snapshot, taskID string) (string, error) {
	if taskID == "" {
		return s.Creation.Objective, nil
	}
	if s.Graph == nil {
		return "", errors.New("writer graph task unavailable")
	}
	task, ok := s.Graph.Graph.Task(taskID)
	if !ok {
		return "", errors.New("writer graph task unavailable")
	}
	question := fmt.Sprintf("[%s] %s | scope: %s | write paths: %s | objective: %s", task.ID, task.Title, strings.Join(task.ScopePaths, ","), strings.Join(task.WritePaths, ","), s.Creation.Objective)
	if len(question) > taskContextFullQueryMax {
		return "", errors.New("writer task context exceeds bound")
	}
	return question, nil
}

func repairSpectrumTask(s Snapshot, invocationTaskID string) (*writerImplementationContext, error) {
	if s.State != "IMPLEMENTING" && s.State != "REPAIRING" {
		return nil, errors.New("repair spectrum requires an active implementation phase")
	}
	if s.Creation.Execution == nil || s.Creation.Execution.RepairIntelligenceVersion != 1 || !taskContextEnabled(s) || s.Graph == nil {
		return nil, errors.New("repair spectrum requires enabled repair intelligence and task context")
	}
	var task *writerImplementationContext
	var err error
	if invocationTaskID == "" {
		task, err = writerImplementationForV4(s)
	} else {
		task, err = writerImplementationForTask(s, invocationTaskID)
	}
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, errors.New("repair spectrum requires a ready repair implementation")
	}
	node, _ := s.Graph.Graph.Task(task.ID)
	gate, ok := s.Graph.Evidence[node.ParentID]
	if node.ParentID == "" || !ok || gate.Outcome != "failed" {
		return nil, errors.New("repair spectrum requires an exact failed parent gate")
	}
	return task, nil
}

func boundedContextSpectrum(s faultlocalization.Spectrum) (faultlocalization.Report, error) {
	r, err := faultlocalization.Analyze(s)
	if err != nil {
		return r, err
	}
	raw, err := canonical.Bytes(s)
	if err != nil {
		return r, err
	}
	if len(raw) > repairContextSpectrumMaxBytes {
		return r, errors.New("repair context spectrum exceeds 128 KiB")
	}
	return r, nil
}

// Replay only consults recorded complete source bytes, never mutable files.
func recordedRepairSpectrum(s Snapshot, rec *TaskContextRecord) (*faultlocalization.Report, error) {
	if rec == nil || rec.RepairSpectrum == nil {
		return nil, nil
	}
	e := rec.RepairSpectrum
	task, err := repairSpectrumTask(s, e.InvocationTaskID)
	if err != nil {
		return nil, err
	}
	node, _ := s.Graph.Graph.Task(task.ID)
	question, err := writerTaskContextQuestion(s, e.InvocationTaskID)
	if err != nil {
		return nil, err
	}
	if rec.IsolationTaskID != "" || rec.Unavailable != "" || rec.Role != writerTaskRole(s) || rec.QueryHash != taskContextQueryHash(question) || rec.QueryLen != len(question) || e.TaskID != task.ID || e.GateID != node.ParentID || e.Spectrum.RunID != s.RunID || e.Spectrum.CandidateID != rec.CandidateID {
		return nil, errors.New("repair spectrum task/context binding mismatch")
	}
	r, err := boundedContextSpectrum(e.Spectrum)
	if err != nil {
		return nil, err
	}
	files := make([]worktree.SourceFile, 0, len(e.Spectrum.Sources))
	for _, source := range e.Spectrum.Sources {
		files = append(files, recordedRepairSource(rec, source.Path))
	}
	if err := validateSpectrumSources(e.Spectrum, r, files); err != nil {
		return nil, err
	}
	r.SourceStatus = "recorded_task_context_bytes_verified"
	return &r, nil
}

func reuseRepairSpectrumContext(existing TaskContextRecord, requested *RepairSpectrumEvidence) (TaskContextRecord, error) {
	if requested == nil {
		return existing, nil
	}
	a, err := canonical.Hash("harness.repair-context-spectrum.v1", requested)
	if err != nil {
		return TaskContextRecord{}, err
	}
	b, err := canonical.Hash("harness.repair-context-spectrum.v1", existing.RepairSpectrum)
	if err != nil {
		return TaskContextRecord{}, err
	}
	if a != b {
		return TaskContextRecord{}, errors.New("task context already frozen; repair spectrum cannot be replaced")
	}
	return existing, nil
}

func writerVisibleTaskContext(rec *TaskContextRecord) *TaskContextRecord {
	if rec == nil || rec.RepairSpectrum == nil {
		return rec
	}
	copy := *rec
	copy.RepairSpectrum = nil
	return &copy
}
