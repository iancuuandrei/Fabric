package control

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/worktree"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

const maxScopeReplanPaths = 8

// ScopeReplanRecord is a candidate-bound, deterministic refinement of one
// implementation task's write set. ScopePaths remain unchanged and continue
// to be the immutable outer ceiling.
type ScopeReplanRecord struct {
	Version            int                   `json:"version"`
	RunID              string                `json:"run_id"`
	PlanID             string                `json:"plan_id"`
	Sequence           int                   `json:"sequence"`
	SourceID           string                `json:"source_id"`
	PreviousGraphHash  string                `json:"previous_graph_hash"`
	CandidateID        string                `json:"candidate_id"`
	CandidateFilesHash string                `json:"candidate_files_hash"`
	CandidateFileCount int                   `json:"candidate_file_count"`
	TaskID             string                `json:"task_id"`
	WriterInvocationID string                `json:"writer_invocation_id"`
	WriterResultHash   string                `json:"writer_result_hash"`
	RequestedPaths     []string              `json:"requested_paths"`
	NextTaskID         string                `json:"next_task_id"`
	GraphHash          string                `json:"graph_hash"`
	Revision           int                   `json:"revision"`
	Graph              engineeringplan.Graph `json:"graph"`
	DesignInvocationID string                `json:"design_invocation_id,omitempty"`
	DesignResultHash   string                `json:"design_result_hash,omitempty"`
	CohortRequestID    string                `json:"cohort_request_id,omitempty"`
}

// SettledGraphWriterCohort is a read-only, exact proof that every member of
// one original static writer cohort reached a terminal scheduler state. It
// contains no write authorization; Changes are only inputs to an explicitly
// approved scope revision and must be freshly prepared on the parent.
type SettledGraphWriterCohort struct {
	Version                int                        `json:"version"`
	Mode                   string                     `json:"mode"`
	ControllerPath         string                     `json:"controller_path"`
	RunID                  string                     `json:"run_id"`
	SourceID               string                     `json:"source_id"`
	GraphDigest            string                     `json:"graph_digest"`
	Revision               int                        `json:"revision"`
	CandidateID            string                     `json:"candidate_id"`
	CandidateFilesHash     string                     `json:"candidate_files_hash"`
	CandidateFileCount     int                        `json:"candidate_file_count"`
	ScheduleID             string                     `json:"schedule_id"`
	ScheduleDefinitionHash string                     `json:"schedule_definition_hash"`
	ScheduleJournalHead    string                     `json:"schedule_journal_head"`
	Members                []SettledGraphWriterMember `json:"members"`
}

// SettledGraphWriterMember binds one exact claim and terminal observation to
// its original task, runtime receipt, and fully validated proposal changes.
// RejectedRecord is present only when the ordinary result projection could
// not be recorded because the proposal needs an ownership replan.
type SettledGraphWriterMember struct {
	TaskID           string                         `json:"task_id"`
	Claim            taskscheduler.Claim            `json:"claim"`
	ClaimID          string                         `json:"claim_id"`
	Evidence         taskscheduler.Evidence         `json:"evidence"`
	Invocation       runtime.Invocation             `json:"invocation"`
	ResultInvocation *runtime.Invocation            `json:"result_invocation,omitempty"`
	ResultEvidence   *taskscheduler.Evidence        `json:"result_evidence,omitempty"`
	RuntimeReceipt   WriterRuntimeReceipt           `json:"runtime_receipt"`
	ResultHash       string                         `json:"result_hash"`
	Corrections      []SettledGraphWriterCorrection `json:"corrections,omitempty"`
	Changes          []fileeffects.Change           `json:"changes"`
	ScopeViolation   bool                           `json:"scope_violation"`
	RejectedResult   *runtime.Result                `json:"rejected_result,omitempty"`
	PreimageFiles    []worktree.FileState           `json:"preimage_files,omitempty"`
	EditPreimages    []WriterEditPreimage           `json:"edit_preimages,omitempty"`
}

// SettledGraphWriterCorrection binds one exact semantic-correction turn back
// to the original static writer claim. It is evidence only; the original
// static claim remains the cohort member's ownership identity.
type SettledGraphWriterCorrection struct {
	CorrectionHash string                    `json:"correction_hash"`
	Dynamic        taskscheduler.DynamicTask `json:"dynamic"`
	Claim          taskscheduler.Claim       `json:"claim"`
	ClaimID        string                    `json:"claim_id"`
	Evidence       taskscheduler.Evidence    `json:"evidence"`
	Invocation     runtime.Invocation        `json:"invocation"`
	ResultHash     string                    `json:"result_hash"`
	ReceiptHead    string                    `json:"receipt_head"`
}

// ScopeReplanTaskUpdate records the explicit old-to-new task identity mapping
// for a graph revision. Empty RequestedPaths are allowed only when an isolated
// cohort must be rebound to a new graph revision without reusing child IDs.
type ScopeReplanTaskUpdate struct {
	TaskID         string   `json:"task_id"`
	NextTaskID     string   `json:"next_task_id"`
	RequestedPaths []string `json:"requested_paths,omitempty"`
}

// ScopeReplanRequest is durable, known-terminal input to the optional design
// turn. Its changes are evidence only; the later graph-writer bridge prepares
// a new parent-side aggregate against the then-current candidate.
type ScopeReplanRequest struct {
	Version        int                      `json:"version"`
	RequestID      string                   `json:"request_id"`
	Sequence       int                      `json:"sequence"`
	RunID          string                   `json:"run_id"`
	PlanID         string                   `json:"plan_id"`
	SourceID       string                   `json:"source_id"`
	GraphDigest    string                   `json:"graph_digest"`
	Revision       int                      `json:"revision"`
	CandidateID    string                   `json:"candidate_id"`
	Updates        []ScopeReplanTaskUpdate  `json:"updates"`
	RequestedPaths []string                 `json:"requested_paths"`
	GraphHash      string                   `json:"graph_hash"`
	NextRevision   int                      `json:"next_revision"`
	Graph          engineeringplan.Graph    `json:"graph"`
	Cohort         SettledGraphWriterCohort `json:"cohort"`
}

func scopeReplanningEnabled(s Snapshot) bool {
	return s.Creation.Execution != nil && s.Creation.Execution.ScopeReplanVersion == 1
}

func cohortScopeReplanningEnabled(s Snapshot) bool {
	return s.Creation.Execution != nil && s.Creation.Execution.ScopeReplanVersion == 2
}

func graphWriterProposalForTask(s Snapshot, taskID string) ([]fileeffects.Change, string, string, error) {
	if taskID == "" || s.WriterProposal == nil {
		return nil, "", "", errors.New("scope replan requires an exact completed writer result")
	}
	i := s.WriterProposal.Invocation
	if i.ID == "" {
		return nil, "", "", errors.New("scope replan writer invocation mismatch")
	}
	// Scope replanning is admitted only for the serial writer path. Its
	// invocation is built from the unique ready implementation selected by the
	// legacy (un-task-bound) writer builder.
	writerSnapshot := scopeReplanWriterSnapshot(s)
	expected, err := writerInvocationForTask(writerSnapshot, "")
	if err != nil || expected != i {
		return nil, "", "", errors.Join(errors.New("scope replan writer is not bound to the active task"), err)
	}
	resultHash, err := canonical.Hash("harness.writer-result.v1", s.WriterProposal.Result)
	if err != nil {
		return nil, "", "", err
	}
	if i.Profile.Runtime == "opencode-http" {
		if err := requireOpenCodeRoleReceipt(s, i, s.WriterProposal.Result); err != nil {
			return nil, "", "", err
		}
	} else if s.WriterHost == nil || s.WriterHost.RuntimeReceipt == nil || s.WriterHost.Intent.Invocation != i || s.WriterHost.RuntimeReceipt.InvocationID != i.ID || s.WriterHost.RuntimeReceipt.ThreadID == "" || s.WriterHost.RuntimeReceipt.TurnID == "" || resultHash != s.WriterHost.RuntimeReceipt.ResultHash {
		return nil, "", "", errors.Join(errors.New("scope replan writer result is not runtime-bound"), err)
	}
	if s.WriterProposal.Prepared.Proposal.Before != *s.Candidate {
		return nil, "", "", errors.New("scope replan writer proposal candidate mismatch")
	}
	return s.WriterProposal.Prepared.Proposal.Changes, i.ID, resultHash, nil
}

func buildScopeReplanRecord(s Snapshot) (ScopeReplanRecord, error) {
	if cohortScopeReplanningEnabled(s) {
		return buildScopeReplanCohortRecord(s)
	}
	record, err := buildScopeReplanRecordBase(s)
	if err != nil {
		return record, err
	}
	if s.Creation.Execution.ScopeReplanDesignVersion == 1 {
		if err := bindScopeReplanDesign(s, &record); err != nil {
			return ScopeReplanRecord{}, err
		}
	}
	return record, nil
}

func buildScopeReplanCohortRecord(s Snapshot) (ScopeReplanRecord, error) {
	var empty ScopeReplanRecord
	policy := s.Creation.Execution
	if policy == nil || policy.ScopeReplanVersion != 2 || policy.Validate() != nil || s.State != "IMPLEMENTING" || s.Graph == nil || s.Plan == nil || s.PlanID != s.Graph.PlanID || s.ApprovedBy == "" || s.Candidate == nil || s.Workspace == nil || s.WorkspaceOutcome != "CONFIRMED" || s.FileIntent != nil || s.FileOutcome == "UNKNOWN" || s.FileOutcome == "PENDING" {
		return empty, errors.New("cohort scope revision requires an opted-in pristine candidate")
	}
	if err := autonomousDispatchBlocked(s); err != nil {
		return empty, err
	}
	if len(s.ScopeReplanRequests) != len(s.ScopeReplans)+1 || len(s.ScopeReplans) >= policy.MaxScopeReplans {
		return empty, errors.New("cohort scope revision has no pending request or its budget is exhausted")
	}
	request := s.ScopeReplanRequests[len(s.ScopeReplanRequests)-1]
	prior := s
	prior.ScopeReplanRequests = append([]ScopeReplanRequest(nil), s.ScopeReplanRequests[:len(s.ScopeReplanRequests)-1]...)
	expected, err := buildScopeReplanRequest(prior, request.Cohort)
	if err != nil || !sameCanonical(request, expected) {
		return empty, errors.Join(errors.New("pending cohort scope request no longer matches its bound evidence"), err)
	}
	candidateID, candidateErr := s.Candidate.ID()
	if candidateErr != nil || request.Sequence != len(s.ScopeReplans)+1 || request.GraphDigest != s.Graph.Digest || request.Revision != s.Graph.Revision || request.CandidateID != candidateID {
		return empty, errors.New("pending cohort scope request is stale")
	}
	if err := engineeringplan.ValidateAutonomousRevision(s.Graph.Graph, request.Graph); err != nil {
		return empty, err
	}
	graphHash, err := engineeringplan.Digest(request.Graph)
	if err != nil || graphHash != request.GraphHash || request.NextRevision != s.Graph.Revision+1 {
		return empty, errors.Join(errors.New("pending cohort scope graph digest mismatch"), err)
	}
	question, err := scopeReplanCohortDesignQuestion(s)
	if err != nil {
		return empty, err
	}
	var design *ExplorerRecord
	for index := range s.Explorations {
		if s.Explorations[index].Question == question {
			if design != nil {
				return empty, ErrAutonomousUnsafe
			}
			design = &s.Explorations[index]
		}
	}
	if design == nil {
		return empty, errors.New("cohort scope revision requires its completed design")
	}
	if err := runtime.ValidateResult(design.Invocation, design.Result, true); err != nil {
		return empty, errors.Join(ErrAutonomousUnsafe, err)
	}
	var decision Exploration
	if err := canonical.Decode([]byte(design.Result.Output), &decision); err != nil || decision.CandidateID != request.CandidateID || !stringListsEqual(decision.Paths, request.RequestedPaths) {
		return empty, errors.Join(errors.New("cohort scope design did not approve the exact request"), err)
	}
	resultHash, err := canonical.Hash("harness.explorer-result.v1", design.Result)
	if err != nil {
		return empty, err
	}
	var primary *SettledGraphWriterMember
	for index := range request.Cohort.Members {
		member := &request.Cohort.Members[index]
		if member.ScopeViolation {
			primary = member
			break
		}
	}
	if primary == nil || len(request.Updates) == 0 {
		return empty, errors.New("cohort scope revision lacks a rejected ownership proposal")
	}
	nextTaskID := ""
	for _, update := range request.Updates {
		if update.TaskID == primary.TaskID {
			nextTaskID = update.NextTaskID
			break
		}
	}
	if nextTaskID == "" {
		return empty, errors.New("cohort scope revision lacks the rejected task mapping")
	}
	resultInvocation := primary.Invocation
	if primary.ResultInvocation != nil {
		resultInvocation = *primary.ResultInvocation
	}
	sourceID, err := s.Creation.Repository.ID()
	if err != nil || sourceID != request.SourceID {
		return empty, errors.Join(errors.New("cohort scope source identity changed"), err)
	}
	return ScopeReplanRecord{
		Version: 2, RunID: s.RunID, PlanID: s.Graph.PlanID, Sequence: request.Sequence,
		SourceID: sourceID, PreviousGraphHash: s.Graph.Digest, CandidateID: request.CandidateID,
		CandidateFilesHash: s.Candidate.FilesHash, CandidateFileCount: s.Candidate.FileCount,
		TaskID: primary.TaskID, WriterInvocationID: resultInvocation.ID, WriterResultHash: primary.ResultHash,
		RequestedPaths: request.RequestedPaths, NextTaskID: nextTaskID, GraphHash: request.GraphHash,
		Revision: request.NextRevision, Graph: request.Graph, DesignInvocationID: design.Invocation.ID,
		DesignResultHash: resultHash, CohortRequestID: request.RequestID,
	}, nil
}

func buildScopeReplanRecordBase(s Snapshot) (ScopeReplanRecord, error) {
	var empty ScopeReplanRecord
	if !scopeReplanningEnabled(s) || s.Creation.Execution.Validate() != nil || s.Graph == nil || s.Graph.PlanID != s.PlanID || s.Plan == nil || s.ApprovedBy == "" || s.Candidate == nil || s.Workspace == nil || s.WorkspaceOutcome != "CONFIRMED" || (s.State != "IMPLEMENTING" && s.State != "REPAIRING") {
		return empty, errors.New("scope replan requires an opted-in graph writer and confirmed candidate")
	}
	if err := autonomousDispatchBlocked(s); err != nil {
		return empty, err
	}
	if len(s.ScopeReplans) >= s.Creation.Execution.MaxScopeReplans {
		return empty, errors.New("scope replan budget exhausted")
	}
	if err := s.Graph.Graph.Validate(); err != nil {
		return empty, err
	}
	task, ok := graphImplementationTask(s)
	if !ok || task.Completed || len(task.Attempts) != 0 {
		return empty, errors.New("scope replan requires the exact unstarted active implementation")
	}
	ready, err := graphReadyTasks(s)
	if err != nil {
		return empty, err
	}
	readyActive := false
	readyImplementations := 0
	for _, candidate := range ready {
		if candidate.Kind == engineeringplan.Implementation {
			readyImplementations++
			if candidate.ID == task.ID {
				readyActive = true
			}
		}
	}
	if !readyActive || readyImplementations != 1 {
		return empty, errors.New("scope replan requires the unique ready implementation task")
	}
	changes, invocationID, resultHash, err := graphWriterProposalForTask(s, task.ID)
	if err != nil {
		return empty, err
	}
	requested, err := scopeReplanRequestedPaths(task, changes)
	if err != nil {
		return empty, err
	}
	if len(requested) == 0 || len(requested) > maxScopeReplanPaths {
		return empty, errors.New("scope replan requires one to eight new write paths")
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return empty, err
	}
	sourceID, err := s.Creation.Repository.ID()
	if err != nil {
		return empty, err
	}
	nextTaskID, err := scopeReplanTaskID(s, task.ID, candidateID, requested)
	if err != nil {
		return empty, err
	}
	next, err := buildScopeReplannedGraph(s.Graph.Graph, task, nextTaskID, requested)
	if err != nil {
		return empty, err
	}
	if err := engineeringplan.ValidateAutonomousRevision(s.Graph.Graph, next); err != nil {
		return empty, err
	}
	graphHash, err := engineeringplan.Digest(next)
	if err != nil {
		return empty, err
	}
	return ScopeReplanRecord{
		Version: 1, RunID: s.RunID, PlanID: s.Graph.PlanID, Sequence: len(s.ScopeReplans) + 1,
		SourceID: sourceID, PreviousGraphHash: s.Graph.Digest, CandidateID: candidateID,
		CandidateFilesHash: s.Candidate.FilesHash, CandidateFileCount: s.Candidate.FileCount,
		TaskID: task.ID, WriterInvocationID: invocationID, WriterResultHash: resultHash,
		RequestedPaths: requested, NextTaskID: nextTaskID, GraphHash: graphHash,
		Revision: s.Graph.Revision + 1, Graph: next,
	}, nil
}

func scopeReplanRequestedPaths(task engineeringplan.Task, changes []fileeffects.Change) ([]string, error) {
	if len(changes) == 0 {
		return nil, errors.New("scope replan writer proposal has no changes")
	}
	requestedSet := map[string]bool{}
	for _, change := range changes {
		if err := safepath.Writable(change.Path); err != nil || !scopeContainsPath(task.ScopePaths, change.Path) {
			return nil, errors.New("scope replan writer proposal escapes immutable scope")
		}
		if !graphTaskPathAdmitted(task.WritePaths, change.Path) {
			requestedSet[change.Path] = true
		}
	}
	requested := make([]string, 0, len(requestedSet))
	for path := range requestedSet {
		requested = append(requested, path)
	}
	sort.Strings(requested)
	return requested, nil
}

func graphTaskPathAdmitted(roots []string, path string) bool {
	for _, root := range roots {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

func scopeContainsPath(roots []string, path string) bool {
	for _, root := range roots {
		if root == "." || path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

func scopeReplanTaskID(s Snapshot, taskID, candidateID string, requested []string) (string, error) {
	material := struct {
		PlanID    string   `json:"plan_id"`
		GraphHash string   `json:"graph_hash"`
		TaskID    string   `json:"task_id"`
		Candidate string   `json:"candidate_id"`
		Sequence  int      `json:"sequence"`
		Paths     []string `json:"paths"`
	}{s.Graph.PlanID, s.Graph.Digest, taskID, candidateID, len(s.ScopeReplans) + 1, requested}
	hash, err := canonical.Hash("harness.graph-scope-replan-task.v1", material)
	if err != nil {
		return "", err
	}
	return "scope-replan-" + hash[:16], nil
}

func buildScopeReplannedGraph(previous engineeringplan.Graph, task engineeringplan.Task, nextTaskID string, requested []string) (engineeringplan.Graph, error) {
	if _, exists := previous.Task(nextTaskID); exists {
		return engineeringplan.Graph{}, errors.New("scope replan task identity collision")
	}
	writePaths := append([]string(nil), task.WritePaths...)
	writePaths = append(writePaths, requested...)
	sort.Strings(writePaths)
	if len(writePaths) > 32 {
		return engineeringplan.Graph{}, errors.New("scope replan write path bound exceeded")
	}
	for i := 1; i < len(writePaths); i++ {
		if writePaths[i] == writePaths[i-1] {
			return engineeringplan.Graph{}, errors.New("scope replan duplicates a write path")
		}
	}
	nextTask := task
	nextTask.ID = nextTaskID
	nextTask.Title = task.Title + " (bounded scope refinement)"
	nextTask.WritePaths = writePaths
	nextTask.Completed = false
	nextTask.Attempts = nil
	graph := previous
	graph.Tasks = make([]engineeringplan.Task, 0, len(previous.Tasks))
	for _, current := range previous.Tasks {
		if current.ID == task.ID {
			graph.Tasks = append(graph.Tasks, nextTask)
			continue
		}
		updated := current
		updated.Dependencies = append([]string(nil), current.Dependencies...)
		for i := range updated.Dependencies {
			if updated.Dependencies[i] == task.ID {
				updated.Dependencies[i] = nextTaskID
			}
		}
		if updated.ParentID == task.ID {
			updated.ParentID = nextTaskID
		}
		graph.Tasks = append(graph.Tasks, updated)
	}
	if err := graph.Validate(); err != nil {
		return engineeringplan.Graph{}, err
	}
	return graph, nil
}

// buildScopeReplannedCohortGraph creates one deterministic revision that
// updates one or more ready implementation tasks while preserving completed
// task records. Callers must separately bind the exact rejected proposal and
// settled cohort evidence before recording this graph.
func buildScopeReplannedCohortGraph(previous engineeringplan.Graph, updates []ScopeReplanTaskUpdate) (engineeringplan.Graph, error) {
	if len(updates) == 0 || len(updates) > 8 {
		return engineeringplan.Graph{}, errors.New("scope replan cohort must replace one to eight implementation tasks")
	}
	tasks := make(map[string]engineeringplan.Task, len(updates))
	nextIDs := make(map[string]bool, len(updates))
	requestedCount := 0
	for _, update := range updates {
		if update.TaskID == "" || update.NextTaskID == "" || update.TaskID == update.NextTaskID || nextIDs[update.NextTaskID] {
			return engineeringplan.Graph{}, errors.New("scope replan cohort task identity is invalid")
		}
		original, ok := previous.Task(update.TaskID)
		if !ok || original.Kind != engineeringplan.Implementation || original.Completed || len(original.Attempts) != 0 {
			return engineeringplan.Graph{}, errors.New("scope replan cohort requires unstarted implementation tasks")
		}
		if _, exists := previous.Task(update.NextTaskID); exists {
			return engineeringplan.Graph{}, errors.New("scope replan cohort task identity collision")
		}
		if _, exists := tasks[update.TaskID]; exists {
			return engineeringplan.Graph{}, errors.New("scope replan cohort duplicates an implementation task")
		}
		nextIDs[update.NextTaskID] = true
		requestedCount += len(update.RequestedPaths)
		if requestedCount > maxScopeReplanPaths {
			return engineeringplan.Graph{}, errors.New("scope replan cohort path bound exceeded")
		}
		writePaths := append([]string(nil), original.WritePaths...)
		for _, requested := range update.RequestedPaths {
			if err := safepath.Writable(requested); err != nil || !scopeContainsPath(original.ScopePaths, requested) || graphTaskPathAdmitted(original.WritePaths, requested) {
				return engineeringplan.Graph{}, errors.New("scope replan cohort path escapes or duplicates immutable task scope")
			}
			writePaths = append(writePaths, requested)
		}
		sort.Strings(writePaths)
		for i := 1; i < len(writePaths); i++ {
			if writePaths[i] == writePaths[i-1] {
				return engineeringplan.Graph{}, errors.New("scope replan cohort duplicates a write path")
			}
		}
		if len(writePaths) > 32 {
			return engineeringplan.Graph{}, errors.New("scope replan cohort write path bound exceeded")
		}
		replacement := original
		replacement.ID = update.NextTaskID
		replacement.Title = original.Title + " (bounded scope refinement)"
		replacement.WritePaths = writePaths
		replacement.Completed = false
		replacement.Attempts = nil
		tasks[update.TaskID] = replacement
	}
	if requestedCount == 0 {
		return engineeringplan.Graph{}, errors.New("scope replan cohort has no requested path refinement")
	}
	graph := previous
	graph.Tasks = make([]engineeringplan.Task, 0, len(previous.Tasks))
	for _, current := range previous.Tasks {
		if replacement, ok := tasks[current.ID]; ok {
			graph.Tasks = append(graph.Tasks, replacement)
			continue
		}
		updated := current
		updated.Dependencies = append([]string(nil), current.Dependencies...)
		for i := range updated.Dependencies {
			if replacement, ok := tasks[updated.Dependencies[i]]; ok {
				updated.Dependencies[i] = replacement.ID
			}
		}
		if replacement, ok := tasks[updated.ParentID]; ok {
			updated.ParentID = replacement.ID
		}
		graph.Tasks = append(graph.Tasks, updated)
	}
	if err := graph.Validate(); err != nil {
		return engineeringplan.Graph{}, err
	}
	return graph, nil
}

func scopeReplanRequestID(request ScopeReplanRequest) (string, error) {
	request.RequestID = ""
	return canonical.Hash("harness.scope-replan-request.v1", request)
}

// InspectSettledGraphWriterCohort validates one exact completed static writer
// schedule and returns only result data already recorded by the runtime. It
// does not dispatch, resume, authorize, or apply any effect.
func InspectSettledGraphWriterCohort(controllerPath, schedulePath string) (SettledGraphWriterCohort, error) {
	var empty SettledGraphWriterCohort
	if !filepath.IsAbs(controllerPath) || filepath.Clean(controllerPath) != controllerPath || !filepath.IsAbs(schedulePath) || filepath.Clean(schedulePath) != schedulePath {
		return empty, errors.New("scope replan journal paths must be clean absolute paths")
	}
	s, err := Inspect(controllerPath)
	if err != nil {
		return empty, err
	}
	if !cohortScopeReplanningEnabled(s) || s.State != "IMPLEMENTING" || s.Graph == nil || s.Candidate == nil || s.Workspace == nil || s.WorkspaceOutcome != "CONFIRMED" || s.GraphWriterBatch != nil || s.FileIntent != nil || s.FileOutcome == "UNKNOWN" || s.FileOutcome == "PENDING" {
		return empty, errors.New("scope replan requires a pristine opted-in graph candidate")
	}
	ready, err := graphReadyTasks(s)
	if err != nil {
		return empty, err
	}
	var tasks []engineeringplan.Task
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			tasks = append(tasks, task)
		}
	}
	if len(tasks) < 2 || len(tasks) > 8 {
		return empty, errors.New("scope replan requires the exact bounded multi-writer cohort")
	}
	specs, err := buildGraphWriterBatchSpecs(controllerPath, s, tasks)
	if err != nil {
		return empty, err
	}
	cohortID, err := graphCohortID(s.Graph.Digest, s.Graph.Revision, specs)
	if err != nil {
		return empty, err
	}
	nonce := "graph-writers-"
	if isolatedImplementationEnabled(s) {
		nonce = "isolated-graph-writers-"
	}
	expectedDefinition := taskscheduler.Definition{Version: 1, Nonce: nonce + cohortID, Tasks: specs}
	scheduleID, err := expectedDefinition.ID()
	if err != nil {
		return empty, err
	}
	expectedPath := controllerPath + ".graph-writers-" + cohortID[:16] + ".jsonl"
	if isolatedImplementationEnabled(s) {
		expectedPath = controllerPath + ".isolated-graph-writers-" + cohortID[:16] + ".jsonl"
	}
	if schedulePath != expectedPath {
		return empty, errors.New("scope replan schedule path differs from the exact static cohort")
	}
	schedule, err := taskscheduler.Inspect(schedulePath)
	if err != nil || schedule.Definition == nil || schedule.ScheduleID != scheduleID || !sameCanonical(*schedule.Definition, expectedDefinition) || len(schedule.Tasks) != len(specs)+len(schedule.Dynamic) {
		return empty, errors.Join(errors.New("scope replan schedule definition or membership mismatch"), err)
	}
	scheduleEvents, err := journal.Read(schedulePath)
	if err != nil || len(scheduleEvents) == 0 {
		return empty, errors.Join(errors.New("scope replan schedule has no validated journal head"), err)
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return empty, err
	}
	parentLease, err := worktree.AcquireRead(s.Workspace.Request)
	if err != nil {
		return empty, err
	}
	defer parentLease.Close()
	parentCandidate, _, err := worktree.Capture(context.Background(), *s.Workspace)
	if err != nil || parentCandidate != *s.Candidate {
		return empty, errors.Join(errors.New("scope replan parent candidate changed"), err)
	}
	taskByID := make(map[string]engineeringplan.Task, len(tasks))
	for _, task := range tasks {
		taskByID[task.ID] = task
	}
	cohort := SettledGraphWriterCohort{
		Version: 1, Mode: cohortScopeReplanMode(*s.Creation.Execution), ControllerPath: controllerPath,
		RunID: s.RunID, CandidateID: candidateID, CandidateFilesHash: s.Candidate.FilesHash, CandidateFileCount: s.Candidate.FileCount,
		GraphDigest: s.Graph.Digest, Revision: s.Graph.Revision, ScheduleID: schedule.ScheduleID,
		ScheduleDefinitionHash: scheduleID, ScheduleJournalHead: scheduleEvents[len(scheduleEvents)-1].Hash,
	}
	sourceID, err := s.Creation.Repository.ID()
	if err != nil {
		return empty, err
	}
	cohort.SourceID = sourceID
	seenCorrectionTasks := make(map[string]bool)
	for _, spec := range specs {
		task := taskByID[spec.ID]
		state, ok := schedule.Tasks[spec.ID]
		if !ok || state.Claim == nil || state.Evidence == nil || !sameCanonical(state.Claim.Task, spec) || state.Claim.ScheduleID != scheduleID || state.Status != taskscheduler.StatusSucceeded && state.Status != taskscheduler.StatusFailed || state.Evidence.Status != state.Status || state.Evidence.RunID != s.RunID || state.Evidence.InvocationID != spec.InvocationID {
			return empty, errors.New("scope replan writer cohort is incomplete or not terminal")
		}
		claimID, err := state.Claim.ID()
		if err != nil {
			return empty, err
		}
		invocation, err := writerInvocationForTask(s, task.ID)
		if err != nil || invocation.ID != spec.InvocationID {
			return empty, errors.Join(errors.New("scope replan writer invocation differs from schedule"), err)
		}
		host, ok := s.GraphWriterHosts[task.ID]
		if !ok || host.Intent.Invocation != invocation || host.RuntimeReceipt == nil || host.RuntimeReceipt.InvocationID != invocation.ID {
			return empty, errors.New("scope replan member lacks exact runtime receipt")
		}
		runtimePath := filepath.Join(host.Intent.Launch.Root, "writer.jsonl")
		runtimeState, runtimeHead, err := codexruntime.InspectWithHead(runtimePath)
		if err != nil || runtimeState.Intent == nil || runtimeState.Intent.Invocation != invocation || runtimeState.Result == nil || runtimeHead != host.RuntimeReceipt.JournalHead {
			return empty, errors.Join(errors.New("scope replan member runtime result is not complete at the recorded head"), err)
		}
		result := *runtimeState.Result
		resultHash, err := canonical.Hash("harness.writer-result.v1", result)
		if err != nil || resultHash != host.RuntimeReceipt.ResultHash {
			return empty, errors.Join(errors.New("scope replan member runtime result hash mismatch"), err)
		}
		if err := runtime.ValidateResult(invocation, result, true); err != nil {
			return empty, err
		}
		if s.Creation.Config.Version == 2 {
			if err := requireCompletedModelAccess(s, invocation, runtimeHead, resultHash); err != nil {
				return empty, err
			}
		}
		member := SettledGraphWriterMember{TaskID: task.ID, Claim: *state.Claim, ClaimID: claimID, Evidence: *state.Evidence, Invocation: invocation, RuntimeReceipt: *host.RuntimeReceipt, ResultHash: resultHash}
		resultInvocation := invocation
		resultEvidence := *state.Evidence
		correctionResult, correctionFound, err := inspectSettledWriterCorrectionChain(s, schedule, scheduleID, schedulePath, candidateID, task, invocation, *state.Claim, *state.Evidence, result, runtimeHead, *host.RuntimeReceipt)
		if err != nil {
			return empty, err
		}
		if correctionFound {
			for _, correction := range correctionResult.Chain {
				if seenCorrectionTasks[correction.Dynamic.Task.ID] {
					return empty, errors.New("scheduled writer correction task is duplicated")
				}
				seenCorrectionTasks[correction.Dynamic.Task.ID] = true
			}
			resultInvocation = correctionResult.Invocation
			resultEvidence = correctionResult.Evidence
			result = correctionResult.Result
			resultHash = correctionResult.ResultHash
			member.RuntimeReceipt = correctionResult.Receipt
			member.ResultHash = resultHash
			member.ResultInvocation = &resultInvocation
			member.ResultEvidence = &resultEvidence
			member.Corrections = correctionResult.Chain
		}
		if resultInvocation != invocation {
			if s.Creation.Config.Version == 2 {
				if err := requireCompletedModelAccess(s, resultInvocation, member.RuntimeReceipt.JournalHead, member.ResultHash); err != nil {
					return empty, err
				}
			}
		}
		if resultEvidence.Status == taskscheduler.StatusSucceeded {
			record, exists := s.GraphWriterResults[task.ID]
			storedResult, resultErr := graphWriterRecordResult(record)
			storedHash, hashErr := canonical.Hash("harness.writer-result.v1", storedResult)
			if !exists || resultErr != nil || hashErr != nil || storedHash != resultHash || !sameCanonical(graphWriterRecordInvocation(record), resultInvocation) || !graphWriterChangesWithinScope(task, graphWriterRecordChanges(record)) || !graphWriterChangesWithinTask(task, graphWriterRecordChanges(record)) {
				return empty, errors.New("successful scope replan sibling lacks its exact accepted proposal")
			}
			member.Changes = append([]fileeffects.Change(nil), graphWriterRecordChanges(record)...)
		} else {
			if _, exists := s.GraphWriterResults[task.ID]; exists {
				return empty, errors.New("failed scope replan member unexpectedly has an accepted proposal")
			}
			changes, files, preimages, err := scopeRejectedMemberChanges(context.Background(), controllerPath, s, task, resultInvocation, result)
			if err != nil {
				return empty, err
			}
			member.Changes, member.PreimageFiles, member.EditPreimages = changes, files, preimages
			member.RejectedResult = &result
			member.ScopeViolation = len(changes) > 0 && !graphWriterChangesWithinTask(task, changes)
			if !member.ScopeViolation || !graphWriterChangesWithinScope(task, changes) {
				return empty, errors.New("failed scope replan member is not a valid immutable-scope ownership violation")
			}
		}
		cohort.Members = append(cohort.Members, member)
	}
	if len(seenCorrectionTasks) != len(schedule.Dynamic) {
		return empty, errors.New("scope replan schedule contains an unbound dynamic task")
	}
	finalCandidate, _, err := worktree.Capture(context.Background(), *s.Workspace)
	if err != nil || finalCandidate != *s.Candidate {
		return empty, errors.Join(errors.New("scope replan parent candidate drifted while reading cohort"), err)
	}
	return cohort, nil
}

type settledWriterCorrectionResult struct {
	Chain      []SettledGraphWriterCorrection
	Invocation runtime.Invocation
	Evidence   taskscheduler.Evidence
	Result     runtime.Result
	ResultHash string
	Receipt    WriterRuntimeReceipt
}

// inspectSettledWriterCorrectionChain folds only the exact scheduled
// correction lineage for one static writer task. The static claim remains the
// cohort identity; the returned invocation/result are merely the latest
// completed output for parent-side validation and re-preparation.
func inspectSettledWriterCorrectionChain(s Snapshot, schedule taskscheduler.Snapshot, scheduleID, schedulePath, candidateID string, task engineeringplan.Task, staticInvocation runtime.Invocation, staticClaim taskscheduler.Claim, staticEvidence taskscheduler.Evidence, staticResult runtime.Result, staticHead string, staticReceipt WriterRuntimeReceipt) (settledWriterCorrectionResult, bool, error) {
	var empty settledWriterCorrectionResult
	var records []RoleSemanticCorrection
	for _, correction := range s.RoleCorrections {
		if correction.TaskID == task.ID && correction.ScheduledTaskID != "" && correction.PriorScheduleID == scheduleID {
			records = append(records, correction)
		}
	}
	if len(records) == 0 {
		return empty, false, nil
	}
	if staticEvidence.Status != taskscheduler.StatusFailed || len(records) > 2 {
		return empty, false, errors.New("writer correction chain is not rooted in one bounded failed static claim")
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Attempt < records[j].Attempt })
	base, err := writerInvocationForTaskBase(s, task.ID)
	if err != nil || base.ID == "" {
		return empty, false, errors.Join(errors.New("writer correction base invocation unavailable"), err)
	}
	if staticClaim.Task.ID != task.ID || staticClaim.Task.RunID != s.RunID || staticClaim.Task.Operation != taskscheduler.OperationWriter || staticClaim.Task.InvocationID != staticInvocation.ID || staticClaim.ScheduleID != scheduleID {
		return empty, false, errors.New("static writer claim identity is malformed")
	}
	previousTaskID := task.ID
	previousInvocation := staticInvocation
	previousResult := staticResult
	previousHead := staticHead
	previousClaim := staticClaim
	previousEvidence := staticEvidence
	chain := make([]SettledGraphWriterCorrection, 0, len(records))
	seenIDs := make(map[string]bool, len(records))
	for index, correction := range records {
		if correction.Version != 1 || correction.Attempt != index+1 || correction.CandidateID != candidateID || correction.BaseInvocationID != base.ID || correction.TaskID != task.ID || correction.PriorTaskID != previousTaskID || correction.PriorSchedulePath != schedulePath || correction.PriorScheduleID != scheduleID || correction.PriorScheduleDefinitionHash != scheduleID || correction.PriorClaim == nil || correction.PriorEvidence == nil || !sameCanonical(*correction.PriorClaim, previousClaim) || !sameCanonical(*correction.PriorEvidence, previousEvidence) || correction.Original != previousInvocation || correction.ReceiptHead != previousHead || !sameCanonical(correction.Predecessor, previousResult) {
			return empty, false, errors.New("writer correction record does not extend the exact preceding failed claim")
		}
		if correction.Invocation.Profile.Role != "writer" || correction.ScheduledTaskID == "" || seenIDs[correction.ScheduledTaskID] {
			return empty, false, errors.New("writer correction identity is duplicated or has another role")
		}
		seenIDs[correction.ScheduledTaskID] = true
		priorState, ok := schedule.Tasks[previousTaskID]
		if !ok || priorState.Status != taskscheduler.StatusFailed || priorState.Claim == nil || priorState.Evidence == nil || !sameCanonical(*priorState.Claim, previousClaim) || !sameCanonical(*priorState.Evidence, previousEvidence) {
			return empty, false, errors.New("writer correction predecessor is not the exact failed schedule claim")
		}
		turnInvocation, err := scheduledTurnInvocation(correction.Invocation, taskscheduler.OperationWriter, correction.ScheduledTaskID)
		if err != nil {
			return empty, false, err
		}
		dynamicMatches := 0
		var dynamic taskscheduler.DynamicTask
		for _, candidate := range schedule.Dynamic {
			if candidate.Task.ID == correction.ScheduledTaskID {
				dynamic = candidate
				dynamicMatches++
			}
		}
		expectedTask := taskscheduler.TaskSpec{ID: correction.ScheduledTaskID, RunID: s.RunID, ControllerPath: staticClaim.Task.ControllerPath, Operation: taskscheduler.OperationWriter, InvocationID: turnInvocation.ID}
		if dynamicMatches != 1 || !sameCanonical(dynamic.Task, expectedTask) || dynamic.ParentAgentID == "" || dynamic.AgentID == "" || dynamic.TurnID != correction.ScheduledTaskID || dynamic.TurnSequence < 1 {
			return empty, false, errors.New("writer correction lacks one exact dynamic schedule entry")
		}
		state, ok := schedule.Tasks[correction.ScheduledTaskID]
		turn := taskscheduler.AgentTurnBinding{ParentAgentID: dynamic.ParentAgentID, AgentID: dynamic.AgentID, TurnID: dynamic.TurnID, TurnSequence: dynamic.TurnSequence}
		if !ok || (state.Status != taskscheduler.StatusSucceeded && state.Status != taskscheduler.StatusFailed) || state.Claim == nil || state.Evidence == nil || !sameCanonical(state.Claim.Task, expectedTask) || !sameCanonical(state.Claim.AgentTurn, &turn) || state.Claim.ScheduleID != scheduleID || state.Evidence.Status != state.Status || state.Evidence.RunID != s.RunID || state.Evidence.InvocationID != turnInvocation.ID {
			return empty, false, errors.New("writer correction schedule claim is incomplete or unresolved")
		}
		claimID, err := state.Claim.ID()
		if err != nil {
			return empty, false, err
		}
		_, _, observedInvocation, err := scheduledInvocation(expectedTask, &turn)
		if err != nil || observedInvocation != turnInvocation {
			return empty, false, errors.Join(errors.New("writer correction invocation differs from its dynamic claim"), err)
		}
		host, ok := s.GraphWriterHosts[correction.ScheduledTaskID]
		if !ok || host.Intent.Invocation != turnInvocation || host.RuntimeReceipt == nil || host.RuntimeReceipt.InvocationID != turnInvocation.ID || host.RuntimeReceipt.ThreadID == "" || host.RuntimeReceipt.TurnID == "" {
			return empty, false, errors.New("writer correction lacks its exact runtime receipt")
		}
		runtimePath := filepath.Join(host.Intent.Launch.Root, "writer.jsonl")
		runtimeState, head, err := codexruntime.InspectWithHead(runtimePath)
		if err != nil || runtimeState.Intent == nil || runtimeState.Intent.Invocation != turnInvocation || runtimeState.Result == nil || runtimeState.TurnStatus != "completed" || head != host.RuntimeReceipt.JournalHead {
			return empty, false, errors.Join(errors.New("writer correction runtime result is incomplete at its receipt"), err)
		}
		result := *runtimeState.Result
		resultHash, err := canonical.Hash("harness.writer-result.v1", result)
		if err != nil || resultHash != host.RuntimeReceipt.ResultHash {
			return empty, false, errors.Join(errors.New("writer correction result hash differs from its receipt"), err)
		}
		if err := runtime.ValidateResult(turnInvocation, result, true); err != nil {
			return empty, false, err
		}
		if s.Creation.Config.Version == 2 {
			if err := requireCompletedModelAccess(s, turnInvocation, head, resultHash); err != nil {
				return empty, false, err
			}
		}
		correctionHash, err := canonical.Hash("harness.role-semantic-correction.v1", correction)
		if err != nil {
			return empty, false, err
		}
		chain = append(chain, SettledGraphWriterCorrection{CorrectionHash: correctionHash, Dynamic: dynamic, Claim: *state.Claim, ClaimID: claimID, Evidence: *state.Evidence, Invocation: turnInvocation, ResultHash: resultHash, ReceiptHead: head})
		previousTaskID, previousInvocation = correction.ScheduledTaskID, turnInvocation
		previousResult, previousHead, previousClaim, previousEvidence = result, head, *state.Claim, *state.Evidence
		empty = settledWriterCorrectionResult{Invocation: turnInvocation, Evidence: *state.Evidence, Result: result, ResultHash: resultHash, Receipt: *host.RuntimeReceipt}
	}
	empty.Chain = chain
	return empty, true, nil
}

func validateSettledWriterCorrectionMember(s Snapshot, scheduleID, schedulePath string, member SettledGraphWriterMember) bool {
	if len(member.Corrections) == 0 || member.ResultInvocation == nil || member.ResultEvidence == nil || member.Evidence.Status != taskscheduler.StatusFailed || member.Corrections[0].Claim.Task.ID == "" {
		return false
	}
	expectedCorrections := 0
	for _, correction := range s.RoleCorrections {
		if correction.TaskID == member.TaskID && correction.PriorScheduleID == scheduleID && correction.ScheduledTaskID != "" {
			expectedCorrections++
		}
	}
	if expectedCorrections != len(member.Corrections) {
		return false
	}
	base := member.Invocation
	if base.ID == "" || member.Claim.Task.InvocationID != base.ID {
		return false
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return false
	}
	priorTaskID := member.TaskID
	priorInvocation := member.Invocation
	priorClaim := member.Claim
	priorEvidence := member.Evidence
	priorHost, ok := s.GraphWriterHosts[priorTaskID]
	if !ok || priorHost.RuntimeReceipt == nil || priorHost.Intent.Invocation != priorInvocation {
		return false
	}
	seen := make(map[string]bool, len(member.Corrections))
	for index, link := range member.Corrections {
		if link.Dynamic.Task.ID == "" || seen[link.Dynamic.Task.ID] || link.ClaimID == "" || link.CorrectionHash == "" {
			return false
		}
		seen[link.Dynamic.Task.ID] = true
		var matched *RoleSemanticCorrection
		for correctionIndex := range s.RoleCorrections {
			candidate := &s.RoleCorrections[correctionIndex]
			if candidate.ScheduledTaskID != link.Dynamic.Task.ID {
				continue
			}
			if matched != nil {
				return false
			}
			matched = candidate
		}
		if matched == nil {
			return false
		}
		correction := *matched
		hash, err := canonical.Hash("harness.role-semantic-correction.v1", correction)
		if err != nil || hash != link.CorrectionHash || correction.Version != 1 || correction.Attempt != index+1 || correction.CandidateID != candidateID || correction.BaseInvocationID != base.ID || correction.TaskID != member.TaskID || correction.PriorTaskID != priorTaskID || correction.PriorScheduleID != scheduleID || correction.PriorClaim == nil || correction.PriorEvidence == nil || !sameCanonical(*correction.PriorClaim, priorClaim) || !sameCanonical(*correction.PriorEvidence, priorEvidence) || correction.Original != priorInvocation {
			return false
		}
		if correction.PriorSchedulePath != schedulePath || correction.PriorScheduleDefinitionHash != scheduleID || correction.Invocation.Profile.Role != "writer" {
			return false
		}
		priorResultHash, err := canonical.Hash("harness.writer-result.v1", correction.Predecessor)
		if err != nil || correction.ReceiptHead != priorHost.RuntimeReceipt.JournalHead || priorHost.RuntimeReceipt.ResultHash != priorResultHash {
			return false
		}
		turnInvocation, err := scheduledTurnInvocation(correction.Invocation, taskscheduler.OperationWriter, correction.ScheduledTaskID)
		if err != nil || turnInvocation != link.Invocation {
			return false
		}
		turn := taskscheduler.AgentTurnBinding{ParentAgentID: link.Dynamic.ParentAgentID, AgentID: link.Dynamic.AgentID, TurnID: link.Dynamic.TurnID, TurnSequence: link.Dynamic.TurnSequence}
		if !sameCanonical(link.Dynamic.Task, taskscheduler.TaskSpec{ID: correction.ScheduledTaskID, RunID: s.RunID, ControllerPath: member.Claim.Task.ControllerPath, Operation: taskscheduler.OperationWriter, InvocationID: turnInvocation.ID}) || link.Dynamic.ParentAgentID == "" || link.Dynamic.AgentID == "" || link.Dynamic.TurnID != correction.ScheduledTaskID || link.Dynamic.TurnSequence < 1 || !sameCanonical(link.Claim.Task, link.Dynamic.Task) || !sameCanonical(link.Claim.AgentTurn, &turn) || link.Claim.ScheduleID != scheduleID || (link.Evidence.Status != taskscheduler.StatusSucceeded && link.Evidence.Status != taskscheduler.StatusFailed) || link.Claim.Task.Operation != taskscheduler.OperationWriter {
			return false
		}
		claimID, err := link.Claim.ID()
		if err != nil || claimID != link.ClaimID || link.Evidence.RunID != s.RunID || link.Evidence.InvocationID != turnInvocation.ID || safepath.RequireDigest(link.Evidence.ControllerHead) != nil || safepath.RequireDigest(link.Evidence.AdmissionID) != nil {
			return false
		}
		if index < len(member.Corrections)-1 && link.Evidence.Status != taskscheduler.StatusFailed {
			return false
		}
		host, ok := s.GraphWriterHosts[correction.ScheduledTaskID]
		if !ok || host.Intent.Invocation != turnInvocation || host.RuntimeReceipt == nil || host.RuntimeReceipt.InvocationID != turnInvocation.ID || host.RuntimeReceipt.JournalHead != link.ReceiptHead || host.RuntimeReceipt.ResultHash != link.ResultHash {
			return false
		}
		priorTaskID, priorInvocation, priorClaim, priorEvidence, priorHost = correction.ScheduledTaskID, turnInvocation, link.Claim, link.Evidence, host
	}
	last := member.Corrections[len(member.Corrections)-1]
	return *member.ResultInvocation == last.Invocation && sameCanonical(*member.ResultEvidence, last.Evidence) && member.RuntimeReceipt.InvocationID == last.Invocation.ID && member.RuntimeReceipt.JournalHead == last.ReceiptHead && member.RuntimeReceipt.ResultHash == member.ResultHash && member.ResultHash == last.ResultHash
}

func expectedSettledGraphWriterSchedulePath(controllerPath string, s Snapshot) (string, error) {
	if s.Graph == nil {
		return "", errors.New("graph unavailable for writer schedule binding")
	}
	ready, err := graphReadyTasks(s)
	if err != nil {
		return "", err
	}
	var tasks []engineeringplan.Task
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			tasks = append(tasks, task)
		}
	}
	specs, err := buildGraphWriterBatchSpecs(controllerPath, s, tasks)
	if err != nil {
		return "", err
	}
	cohortID, err := graphCohortID(s.Graph.Digest, s.Graph.Revision, specs)
	if err != nil {
		return "", err
	}
	prefix := ".graph-writers-"
	if isolatedImplementationEnabled(s) {
		prefix = ".isolated-graph-writers-"
	}
	return controllerPath + prefix + cohortID[:16] + ".jsonl", nil
}

func scopeRejectedMemberChanges(ctx context.Context, path string, s Snapshot, task engineeringplan.Task, invocation runtime.Invocation, result runtime.Result) ([]fileeffects.Change, []worktree.FileState, []WriterEditPreimage, error) {
	if isolatedImplementationEnabled(s) {
		binding, err := isolatedWriterBindingForTask(s, task.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		lease, err := worktree.AcquireRead(binding.Workspace.Request)
		if err != nil {
			return nil, nil, nil, err
		}
		defer lease.Close()
		if writercontract.IsAnchoredEdits(s.Creation.Config.WriterContract) {
			anchored, err := decodeAnchoredProposal(result.Output)
			if err != nil {
				return nil, nil, nil, err
			}
			manifest, preimages, err := readIsolatedAnchoredPreimages(ctx, binding, anchored)
			if err != nil {
				return nil, nil, nil, err
			}
			id, err := binding.Candidate.ID()
			if err != nil || anchored.CandidateID != id {
				return nil, nil, nil, errors.Join(errors.New("isolated scope result candidate mismatch"), err)
			}
			changes, err := composeAnchoredProposal(anchored, manifest, preimages)
			return changes, relevantPreimageFiles(manifest, changes), preimages, err
		}
		proposal, err := decodeWriterProposal(s.Creation.Config.WriterContract, result.Output)
		if err != nil {
			return nil, nil, nil, err
		}
		id, err := binding.Candidate.ID()
		if err != nil || proposal.CandidateID != id {
			return nil, nil, nil, errors.Join(errors.New("isolated scope result candidate mismatch"), err)
		}
		return proposal.Changes, nil, nil, nil
	}
	if writercontract.IsAnchoredEdits(s.Creation.Config.WriterContract) {
		anchored, err := decodeAnchoredProposal(result.Output)
		if err != nil {
			return nil, nil, nil, err
		}
		manifest, preimages, err := readAnchoredPreimages(ctx, path, s, anchored)
		if err != nil {
			return nil, nil, nil, err
		}
		id, err := s.Candidate.ID()
		if err != nil || anchored.CandidateID != id {
			return nil, nil, nil, errors.Join(errors.New("parallel scope result candidate mismatch"), err)
		}
		changes, err := composeAnchoredProposal(anchored, manifest, preimages)
		return changes, relevantPreimageFiles(manifest, changes), preimages, err
	}
	proposal, err := decodeWriterProposal(s.Creation.Config.WriterContract, result.Output)
	if err != nil {
		return nil, nil, nil, err
	}
	id, err := s.Candidate.ID()
	if err != nil || proposal.CandidateID != id {
		return nil, nil, nil, errors.Join(errors.New("parallel scope result candidate mismatch"), err)
	}
	return proposal.Changes, nil, nil, nil
}

func relevantPreimageFiles(manifest []worktree.FileState, changes []fileeffects.Change) []worktree.FileState {
	used := make(map[string]bool, len(changes))
	for _, change := range changes {
		if change.BeforeHash != nil {
			used[change.Path] = true
		}
	}
	files := make([]worktree.FileState, 0, len(used))
	for _, file := range manifest {
		if used[file.Path] {
			files = append(files, file)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func graphWriterRecordResult(record GraphWriterRecord) (runtime.Result, error) {
	if record.TaskID == "" {
		return runtime.Result{}, errors.New("writer result record has no task identity")
	}
	if record.Isolated != nil {
		return record.Isolated.Result, nil
	}
	if record.Writer.Invocation.ID == "" {
		return runtime.Result{}, errors.New("writer result record has no invocation")
	}
	return record.Writer.Result, nil
}

func buildScopeReplanRequest(s Snapshot, cohort SettledGraphWriterCohort) (ScopeReplanRequest, error) {
	var empty ScopeReplanRequest
	policy := s.Creation.Execution
	if policy == nil || policy.ScopeReplanVersion != 2 || policy.Validate() != nil || s.State != "IMPLEMENTING" || s.Graph == nil || s.Candidate == nil || s.Workspace == nil || s.FileIntent != nil || s.GraphWriterBatch != nil || s.WorkspaceOutcome != "CONFIRMED" {
		return empty, errors.New("cohort scope replan requires an opted-in pristine parent candidate")
	}
	if len(s.ScopeReplanRequests) != len(s.ScopeReplans) || len(s.ScopeReplans) >= policy.MaxScopeReplans {
		return empty, errors.New("cohort scope replan request is already pending or budget is exhausted")
	}
	if err := autonomousDispatchBlocked(s); err != nil {
		return empty, err
	}
	if s.FileOutcome == "UNKNOWN" || s.FileOutcome == "PENDING" {
		return empty, ErrAutonomousReconciliation
	}
	if cohort.Version != 1 || cohort.Mode != cohortScopeReplanMode(*policy) || !filepath.IsAbs(cohort.ControllerPath) || filepath.Clean(cohort.ControllerPath) != cohort.ControllerPath || len(cohort.Members) == 0 || len(cohort.Members) > 8 {
		return empty, errors.New("settled writer cohort has an invalid version, mode, or size")
	}
	graphHash, err := engineeringplan.Digest(s.Graph.Graph)
	if err != nil || graphHash != s.Graph.Digest {
		return empty, errors.Join(errors.New("cohort scope replan graph digest mismatch"), err)
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return empty, err
	}
	sourceID, err := s.Creation.Repository.ID()
	if err != nil {
		return empty, err
	}
	if cohort.RunID != s.RunID || cohort.SourceID != sourceID || cohort.GraphDigest != s.Graph.Digest || cohort.Revision != s.Graph.Revision || cohort.CandidateID != candidateID || cohort.CandidateFilesHash != s.Candidate.FilesHash || cohort.CandidateFileCount != s.Candidate.FileCount {
		return empty, errors.New("settled writer cohort differs from the current source, graph, or candidate")
	}
	for _, digest := range []string{cohort.ScheduleID, cohort.ScheduleDefinitionHash, cohort.ScheduleJournalHead} {
		if err := safepath.RequireDigest(digest); err != nil {
			return empty, errors.New("settled writer cohort has an invalid schedule binding")
		}
	}
	ready, err := graphReadyTasks(s)
	if err != nil {
		return empty, err
	}
	var tasks []engineeringplan.Task
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			tasks = append(tasks, task)
		}
	}
	if len(tasks) != len(cohort.Members) {
		return empty, errors.New("settled writer cohort is not the complete ready implementation set")
	}
	specs, err := scopeReplanStaticSpecs(s, cohort.ControllerPath, cohort.ScheduleID, tasks, cohort.Members)
	if err != nil {
		return empty, err
	}
	cohortID, err := graphCohortID(s.Graph.Digest, s.Graph.Revision, specs)
	if err != nil {
		return empty, err
	}
	nonce := "graph-writers-"
	if isolatedImplementationEnabled(s) {
		nonce = "isolated-graph-writers-"
	}
	definition := taskscheduler.Definition{Version: 1, Nonce: nonce + cohortID, Tasks: specs}
	scheduleID, err := definition.ID()
	if err != nil || scheduleID != cohort.ScheduleID || scheduleID != cohort.ScheduleDefinitionHash {
		return empty, errors.Join(errors.New("settled writer cohort schedule definition mismatch"), err)
	}
	schedulePrefix := ".graph-writers-"
	if isolatedImplementationEnabled(s) {
		schedulePrefix = ".isolated-graph-writers-"
	}
	schedulePath := cohort.ControllerPath + schedulePrefix + cohortID[:16] + ".jsonl"
	taskByID := make(map[string]engineeringplan.Task, len(tasks))
	for _, task := range tasks {
		taskByID[task.ID] = task
	}
	members := append([]SettledGraphWriterMember(nil), cohort.Members...)
	sort.Slice(members, func(i, j int) bool { return members[i].TaskID < members[j].TaskID })
	if len(members) != len(tasks) {
		return empty, errors.New("settled writer cohort member count changed")
	}
	var updates []ScopeReplanTaskUpdate
	requestedSet := map[string]bool{}
	changed := false
	for index, member := range members {
		task, ok := taskByID[member.TaskID]
		if !ok || index > 0 && members[index-1].TaskID == member.TaskID {
			return empty, errors.New("settled writer cohort has a missing or duplicate task")
		}
		specIndex := sort.Search(len(specs), func(i int) bool { return specs[i].ID >= task.ID })
		if specIndex == len(specs) || specs[specIndex].ID != task.ID {
			return empty, errors.New("settled writer cohort task was not scheduled")
		}
		spec := specs[specIndex]
		if !sameCanonical(member.Claim.Task, spec) || member.Claim.Task.ID != member.TaskID || member.Claim.ScheduleID != scheduleID || member.Claim.Generation < 1 || member.Claim.ControllerHead == "" {
			return empty, errors.New("settled writer cohort claim differs from the exact static task")
		}
		claimID, claimErr := member.Claim.ID()
		if claimErr != nil || claimID != member.ClaimID || safepath.RequireDigest(member.ClaimID) != nil {
			return empty, errors.Join(errors.New("settled writer cohort claim identity mismatch"), claimErr)
		}
		if member.Evidence.RunID != s.RunID || member.Evidence.InvocationID != spec.InvocationID || member.Evidence.Status != taskscheduler.StatusSucceeded && member.Evidence.Status != taskscheduler.StatusFailed || safepath.RequireDigest(member.Evidence.ControllerHead) != nil || safepath.RequireDigest(member.Evidence.AdmissionID) != nil {
			return empty, errors.New("settled writer cohort lacks exact terminal scheduler evidence")
		}
		invocation := member.Invocation
		if invocation.ID != spec.InvocationID {
			return empty, errors.New("settled writer cohort original invocation differs from its static claim")
		}
		resultInvocation := invocation
		resultEvidence := member.Evidence
		resultHostTaskID := task.ID
		if len(member.Corrections) == 0 {
			if member.ResultInvocation != nil || member.ResultEvidence != nil {
				return empty, errors.New("uncorrected cohort member carries correction result bindings")
			}
		} else {
			if member.ResultInvocation == nil || member.ResultEvidence == nil || !validateSettledWriterCorrectionMember(s, scheduleID, schedulePath, member) {
				return empty, errors.New("settled writer correction lineage differs from durable controller evidence")
			}
			last := member.Corrections[len(member.Corrections)-1]
			resultInvocation, resultEvidence, resultHostTaskID = last.Invocation, last.Evidence, last.Dynamic.Task.ID
			if *member.ResultInvocation != resultInvocation || !sameCanonical(*member.ResultEvidence, resultEvidence) {
				return empty, errors.New("settled writer correction result binding differs from its last turn")
			}
		}
		host, ok := s.GraphWriterHosts[resultHostTaskID]
		if !ok || host.Intent.Invocation != resultInvocation || host.RuntimeReceipt == nil || *host.RuntimeReceipt != member.RuntimeReceipt || member.RuntimeReceipt.InvocationID != resultInvocation.ID || member.RuntimeReceipt.JournalHead == "" || member.RuntimeReceipt.ResultHash != member.ResultHash {
			return empty, errors.New("settled writer cohort runtime receipt mismatch")
		}
		for _, digest := range []string{member.RuntimeReceipt.JournalHead, member.ResultHash} {
			if err := safepath.RequireDigest(digest); err != nil {
				return empty, errors.New("settled writer cohort runtime receipt is malformed")
			}
		}
		if s.Creation.Config.Version == 2 {
			if err := requireCompletedModelAccess(s, resultInvocation, member.RuntimeReceipt.JournalHead, member.ResultHash); err != nil {
				return empty, err
			}
		}
		if len(member.Changes) == 0 || !graphWriterChangesWithinScope(task, member.Changes) {
			return empty, errors.New("settled writer changes escape immutable task scope")
		}
		requested, err := scopeReplanRequestedPaths(task, member.Changes)
		if err != nil {
			return empty, err
		}
		if (len(requested) != 0) != member.ScopeViolation || (member.ScopeViolation && resultEvidence.Status != taskscheduler.StatusFailed) || (!member.ScopeViolation && resultEvidence.Status != taskscheduler.StatusSucceeded) {
			return empty, errors.New("settled writer cohort scope classification contradicts task outcome")
		}
		if member.ScopeViolation {
			changed = true
			if err := validateScopeRejectedGraphWriterMember(s, task, member, resultInvocation); err != nil {
				return empty, err
			}
		} else {
			record, ok := s.GraphWriterResults[task.ID]
			if !ok || graphWriterRecordInvocation(record) != resultInvocation || !sameCanonical(graphWriterRecordChanges(record), member.Changes) {
				return empty, errors.New("accepted writer cohort member differs from its recorded proposal")
			}
		}
		for _, path := range requested {
			requestedSet[path] = true
		}
		if member.ScopeViolation || isolatedImplementationEnabled(s) {
			nextID, err := scopeReplanTaskID(s, task.ID, candidateID, requested)
			if err != nil {
				return empty, err
			}
			updates = append(updates, ScopeReplanTaskUpdate{TaskID: task.ID, NextTaskID: nextID, RequestedPaths: requested})
		}
	}
	if !changed {
		return empty, errors.New("settled writer cohort has no ownership expansion to replan")
	}
	requestedPaths := make([]string, 0, len(requestedSet))
	for path := range requestedSet {
		requestedPaths = append(requestedPaths, path)
	}
	sort.Strings(requestedPaths)
	if len(requestedPaths) == 0 || len(requestedPaths) > maxScopeReplanPaths {
		return empty, errors.New("cohort scope replan requires one to eight bounded new paths")
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].TaskID < updates[j].TaskID })
	next, err := buildScopeReplannedCohortGraph(s.Graph.Graph, updates)
	if err != nil {
		return empty, err
	}
	if err := engineeringplan.ValidateAutonomousRevision(s.Graph.Graph, next); err != nil {
		return empty, err
	}
	graphHash, err = engineeringplan.Digest(next)
	if err != nil {
		return empty, err
	}
	request := ScopeReplanRequest{
		Version: 1, Sequence: len(s.ScopeReplanRequests) + 1, RunID: s.RunID, PlanID: s.Graph.PlanID,
		SourceID: sourceID, GraphDigest: s.Graph.Digest, Revision: s.Graph.Revision, CandidateID: candidateID,
		Updates: updates, RequestedPaths: requestedPaths, GraphHash: graphHash, NextRevision: s.Graph.Revision + 1, Graph: next, Cohort: cohort,
	}
	request.RequestID, err = scopeReplanRequestID(request)
	if err != nil {
		return empty, err
	}
	return request, nil
}

func cohortScopeReplanMode(policy ExecutionPolicy) string {
	if policy.IsolatedImplementationVersion == 1 {
		return "isolated"
	}
	if policy.IsolatedImplementationVersion == 2 {
		return "isolated-waves-unsupported"
	}
	return "parallel"
}

func graphWriterChangesWithinScope(task engineeringplan.Task, changes []fileeffects.Change) bool {
	if len(changes) == 0 {
		return false
	}
	for _, change := range changes {
		if safepath.Writable(change.Path) != nil || !scopeContainsPath(task.ScopePaths, change.Path) {
			return false
		}
	}
	return true
}

// scopeReplanStaticSpecs reconstructs the immutable scheduler definition from
// the original claims sealed into the cohort. Role context can legitimately
// evolve after those claims (for example, when a semantic correction turn is
// admitted), so replay must not rederive the original invocation IDs from the
// later controller snapshot.
func scopeReplanStaticSpecs(s Snapshot, controllerPath, scheduleID string, tasks []engineeringplan.Task, members []SettledGraphWriterMember) ([]taskscheduler.TaskSpec, error) {
	if len(tasks) == 0 || len(tasks) != len(members) {
		return nil, errors.New("settled writer cohort task set is incomplete")
	}
	taskByID := make(map[string]engineeringplan.Task, len(tasks))
	for _, task := range tasks {
		if task.ID == "" {
			return nil, errors.New("settled writer cohort has an empty task identity")
		}
		if _, exists := taskByID[task.ID]; exists {
			return nil, errors.New("settled writer cohort has duplicate ready tasks")
		}
		taskByID[task.ID] = task
	}
	specs := make([]taskscheduler.TaskSpec, 0, len(members))
	seen := make(map[string]bool, len(members))
	for _, member := range members {
		if member.TaskID == "" || seen[member.TaskID] {
			return nil, errors.New("settled writer cohort has duplicate or empty member tasks")
		}
		seen[member.TaskID] = true
		task, ok := taskByID[member.TaskID]
		if !ok {
			return nil, errors.New("settled writer cohort member is not currently ready")
		}
		claimTask := member.Claim.Task
		if claimTask.ID != member.TaskID || claimTask.RunID != s.RunID || claimTask.ControllerPath != controllerPath || claimTask.Operation != taskscheduler.OperationWriter || claimTask.InvocationID != member.Invocation.ID || member.Invocation.ID == "" || member.Claim.ScheduleID != scheduleID {
			return nil, errors.New("settled writer cohort claim does not bind its original invocation")
		}
		// All task fields are fixed by this static writer operation.
		want := taskscheduler.TaskSpec{ID: task.ID, RunID: s.RunID, ControllerPath: controllerPath, Operation: taskscheduler.OperationWriter, InvocationID: member.Invocation.ID}
		if !sameCanonical(claimTask, want) {
			return nil, errors.New("settled writer cohort claim contains unexpected task fields")
		}
		specs = append(specs, claimTask)
	}
	if len(seen) != len(taskByID) {
		return nil, errors.New("settled writer cohort omits a ready implementation task")
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	return specs, nil
}

func validateScopeRejectedGraphWriterMember(s Snapshot, task engineeringplan.Task, member SettledGraphWriterMember, resultInvocation runtime.Invocation) error {
	if member.RejectedResult == nil {
		return errors.New("scope-rejected writer evidence lacks its completed runtime result")
	}
	if _, exists := s.GraphWriterResults[task.ID]; exists {
		return errors.New("scope-rejected writer evidence is duplicated by an accepted proposal record")
	}
	result := *member.RejectedResult
	var changes []fileeffects.Change
	if isolatedImplementationEnabled(s) {
		binding, err := isolatedWriterBindingForTask(s, task.ID)
		if err != nil {
			return err
		}
		if err := member.Invocation.Validate(); err != nil || member.Invocation.Profile.Role != "writer" || member.Claim.Task.InvocationID != member.Invocation.ID {
			return errors.Join(errors.New("isolated scope rejection does not match its sealed child invocation"), err)
		}
		if err := runtime.ValidateResult(resultInvocation, result, true); err != nil {
			return err
		}
		childID, err := binding.Candidate.ID()
		if err != nil {
			return err
		}
		if writercontract.IsAnchoredEdits(s.Creation.Config.WriterContract) {
			anchored, err := decodeAnchoredProposal(result.Output)
			if err != nil || anchored.CandidateID != childID {
				return errors.Join(errors.New("isolated anchored scope rejection candidate mismatch"), err)
			}
			changes, err = composeAnchoredProposal(anchored, member.PreimageFiles, member.EditPreimages)
			if err != nil {
				return err
			}
		} else {
			if len(member.PreimageFiles) != 0 || len(member.EditPreimages) != 0 {
				return errors.New("non-anchored isolated scope result contains unexpected preimages")
			}
			decoded, err := decodeWriterProposal(s.Creation.Config.WriterContract, result.Output)
			if err != nil || decoded.CandidateID != childID {
				return errors.Join(errors.New("isolated proposal scope rejection candidate mismatch"), err)
			}
			changes = decoded.Changes
		}
	} else {
		expected := member.Invocation
		if member.ResultInvocation != nil {
			expected = *member.ResultInvocation
		}
		if resultInvocation != expected {
			return errors.New("parallel scope rejection result invocation mismatch")
		}
		if err := runtime.ValidateResult(resultInvocation, result, true); err != nil {
			return err
		}
		if err := requireOpenCodeRoleReceipt(s, resultInvocation, result); err != nil {
			return err
		}
		candidateID, err := s.Candidate.ID()
		if err != nil {
			return err
		}
		if writercontract.IsAnchoredEdits(s.Creation.Config.WriterContract) {
			anchored, err := decodeAnchoredProposal(result.Output)
			if err != nil || anchored.CandidateID != candidateID {
				return errors.Join(errors.New("parallel anchored scope rejection candidate mismatch"), err)
			}
			changes, err = composeAnchoredProposal(anchored, member.PreimageFiles, member.EditPreimages)
			if err != nil {
				return err
			}
		} else {
			if len(member.PreimageFiles) != 0 || len(member.EditPreimages) != 0 {
				return errors.New("non-anchored parallel scope result contains unexpected preimages")
			}
			decoded, err := decodeWriterProposal(s.Creation.Config.WriterContract, result.Output)
			if err != nil || decoded.CandidateID != candidateID {
				return errors.Join(errors.New("parallel writer scope rejection candidate mismatch"), err)
			}
			changes = decoded.Changes
		}
	}
	resultHash, err := canonical.Hash("harness.writer-result.v1", result)
	if err != nil || resultHash != member.ResultHash || !sameCanonical(changes, member.Changes) {
		return errors.Join(errors.New("scope-rejected writer changes differ from its completed result"), err)
	}
	return nil
}

func replayGraphScopeReplanRequest(s *Snapshot, e journal.Event) error {
	var request ScopeReplanRequest
	if err := canonical.Decode(e.Payload, &request); err != nil {
		return err
	}
	expected, err := buildScopeReplanRequest(*s, request.Cohort)
	if err != nil {
		return err
	}
	if !sameCanonical(request, expected) {
		return errors.New("scope replan request differs from settled cohort evidence")
	}
	s.ScopeReplanRequests = append(s.ScopeReplanRequests, request)
	return nil
}

func replayGraphScopeReplan(s *Snapshot, e journal.Event) error {
	var record ScopeReplanRecord
	if err := canonical.Decode(e.Payload, &record); err != nil {
		return err
	}
	expected, err := buildScopeReplanRecord(*s)
	if err != nil {
		return err
	}
	if !sameCanonical(record, expected) {
		return errors.New("scope replan record differs from confirmed writer proposal")
	}
	s.Graph.Graph = record.Graph
	s.Graph.Digest = record.GraphHash
	s.Graph.Revision = record.Revision
	s.ScopeReplans = append(s.ScopeReplans, record)
	return nil
}

// recordGraphScopeReplanCohort durably captures the exact settled cohort
// before any design invocation, then records one deterministic graph revision.
// The cohort changes are evidence only: subsequent writer integration must
// freshly prepare the selected changes against the current parent candidate.
func recordGraphScopeReplanCohort(ctx context.Context, path string, bound Snapshot, cohort SettledGraphWriterCohort) (result Snapshot, failure error) {
	latest, err := Inspect(path)
	if err != nil {
		return bound, err
	}
	if latest.Graph == nil || bound.Graph == nil || latest.Candidate == nil || bound.Candidate == nil || latest.Graph.Digest != bound.Graph.Digest || latest.Graph.Revision != bound.Graph.Revision || *latest.Candidate != *bound.Candidate || latest.State != "IMPLEMENTING" {
		return latest, errors.New("cohort scope request binding changed before durable admission")
	}
	if cohort.ControllerPath != path {
		return latest, errors.New("settled cohort controller path mismatch")
	}
	schedulePath, err := expectedSettledGraphWriterSchedulePath(path, latest)
	if err != nil {
		return latest, err
	}
	verifiedCohort, err := InspectSettledGraphWriterCohort(path, schedulePath)
	if err != nil || !sameCanonical(cohort, verifiedCohort) {
		return latest, errors.Join(errors.New("settled writer cohort changed before durable admission"), err)
	}
	cohort = verifiedCohort
	request, err := buildScopeReplanRequest(latest, cohort)
	if err != nil {
		return latest, err
	}
	lease, err := worktree.AcquireRead(latest.Workspace.Request)
	if err != nil {
		return latest, err
	}
	leaseClosed := false
	defer func() {
		if !leaseClosed {
			failure = errors.Join(failure, lease.Close())
		}
	}()
	observed, _, err := worktree.Capture(ctx, *latest.Workspace)
	if err != nil || observed != *latest.Candidate {
		return latest, errors.Join(errors.New("cohort scope candidate changed before durable request"), err)
	}
	latest, err = Inspect(path)
	if err != nil {
		return latest, err
	}
	expected, err := buildScopeReplanRequest(latest, cohort)
	if err != nil || !sameCanonical(request, expected) {
		return latest, errors.Join(errors.New("cohort scope request changed during candidate capture"), err)
	}
	if err := Append(path, "graph.scope-replan-requested", request); err != nil {
		latest, inspectErr := Inspect(path)
		if inspectErr != nil || len(latest.ScopeReplanRequests) == 0 || latest.ScopeReplanRequests[len(latest.ScopeReplanRequests)-1].RequestID != request.RequestID {
			return latest, errors.Join(err, inspectErr)
		}
	}
	if err := lease.Close(); err != nil {
		leaseClosed = true
		return InspectOr(latest, path, err)
	}
	leaseClosed = true
	latest, err = Inspect(path)
	if err != nil {
		return latest, err
	}
	if err := releaseGraphMemoryAdmissionForScopeRequest(path, request.RequestID); err != nil {
		return InspectOr(latest, path, err)
	}
	latest, err = Inspect(path)
	if err != nil {
		return latest, err
	}
	if err := ensureScopeReplanCohortDesign(ctx, path, latest); err != nil {
		return InspectOr(latest, path, err)
	}
	latest, err = Inspect(path)
	if err != nil {
		return latest, err
	}
	if latest.Graph == nil || latest.Candidate == nil || latest.Workspace == nil || bound.Candidate == nil || latest.Graph.Digest != bound.Graph.Digest || latest.Graph.Revision != bound.Graph.Revision || *latest.Candidate != *bound.Candidate {
		return latest, errors.New("cohort scope candidate changed while design was running")
	}
	lease, err = worktree.AcquireRead(latest.Workspace.Request)
	if err != nil {
		return latest, err
	}
	leaseClosed = false
	observed, _, err = worktree.Capture(ctx, *latest.Workspace)
	if err != nil || observed != *latest.Candidate {
		return latest, errors.Join(errors.New("cohort scope candidate changed before revision admission"), err)
	}
	latest, err = Inspect(path)
	if err != nil || latest.Graph == nil || latest.Candidate == nil {
		return latest, errors.Join(errors.New("cohort scope binding changed before revision admission"), err)
	}
	latestCandidateID, latestIDErr := latest.Candidate.ID()
	boundCandidateID, boundIDErr := bound.Candidate.ID()
	if latestIDErr != nil || boundIDErr != nil || latest.Graph.Digest != bound.Graph.Digest || latestCandidateID != boundCandidateID {
		return latest, errors.Join(errors.New("cohort scope binding changed before revision admission"), err)
	}
	record, err := buildScopeReplanCohortRecord(latest)
	if err != nil {
		return latest, err
	}
	if err := Append(path, "graph.scope-replanned", record); err != nil {
		return InspectOr(latest, path, err)
	}
	if err := lease.Close(); err != nil {
		leaseClosed = true
		return InspectOr(latest, path, err)
	}
	leaseClosed = true
	return Inspect(path)
}

func recordGraphScopeReplan(ctx context.Context, path string, bound Snapshot) (result Snapshot, failure error) {
	if bound.Creation.Execution != nil && bound.Creation.Execution.ScopeReplanDesignVersion == 1 {
		if err := ensureScopeReplanDesign(ctx, path, bound); err != nil {
			return InspectOr(bound, path, err)
		}
	}
	latest, err := Inspect(path)
	if err != nil {
		return bound, err
	}
	if latest.Graph == nil || bound.Graph == nil || latest.Graph.Digest != bound.Graph.Digest || latest.Candidate == nil || bound.Candidate == nil || latest.Workspace == nil || bound.Workspace == nil || *latest.Workspace != *bound.Workspace || *latest.Candidate != *bound.Candidate {
		return latest, errors.New("scope replan binding changed before admission")
	}
	lease, err := worktree.AcquireRead(latest.Workspace.Request)
	if err != nil {
		return latest, err
	}
	defer func() { failure = errors.Join(failure, lease.Close()) }()
	observed, _, err := worktree.Capture(ctx, *latest.Workspace)
	if err != nil || observed != *latest.Candidate {
		return latest, errors.Join(errors.New("scope replan candidate changed before admission"), err)
	}
	latest, err = Inspect(path)
	if err != nil {
		return latest, err
	}
	if latest.Graph == nil || latest.Candidate == nil || latest.Graph.Digest != bound.Graph.Digest || *latest.Candidate != *bound.Candidate {
		return latest, errors.New("scope replan binding changed during candidate capture")
	}
	record, err := buildScopeReplanRecord(latest)
	if err != nil {
		return latest, err
	}
	if err := Append(path, "graph.scope-replanned", record); err != nil {
		return InspectOr(latest, path, err)
	}
	return Inspect(path)
}
