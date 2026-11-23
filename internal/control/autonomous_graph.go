package control

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexhost"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

var graphAttemptIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func graphCohortID(digest string, revision int, specs []taskscheduler.TaskSpec) (string, error) {
	ordered := append([]taskscheduler.TaskSpec(nil), specs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	return canonical.Hash("harness.graph-cohort.v1", struct {
		Digest   string                   `json:"digest"`
		Revision int                      `json:"revision"`
		Tasks    []taskscheduler.TaskSpec `json:"tasks"`
	}{digest, revision, ordered})
}

// GraphTaskEvidence preserves actual runtime/candidate/verification/review
// evidence IDs for one durable graph task. Model claims alone never count.
type GraphTaskEvidence struct {
	TaskID               string `json:"task_id"`
	AttemptID            string `json:"attempt_id"`
	Outcome              string `json:"outcome"`
	ExplorerInvocationID string `json:"explorer_invocation_id,omitempty"`
	CandidateID          string `json:"candidate_id,omitempty"`
	WriterInvocationID   string `json:"writer_invocation_id,omitempty"`
	VerificationPlanID   string `json:"verification_plan_id,omitempty"`
	ReviewInvocationID   string `json:"review_invocation_id,omitempty"`
}

// GraphState is runner-owned progress bound to the accepted planner result.
// The accepted Plan (PlanID) remains the evidence; Digest/Revision key the
// exact graph bytes so substitution is rejected.
type GraphState struct {
	PlanID   string                       `json:"plan_id"`
	Digest   string                       `json:"digest"`
	Revision int                          `json:"revision"`
	Graph    engineeringplan.Graph        `json:"graph"`
	Evidence map[string]GraphTaskEvidence `json:"evidence,omitempty"`
}

// GraphRecord persists the initial accepted graph (revision 1).
type GraphRecord struct {
	Version  int                   `json:"version"`
	PlanID   string                `json:"plan_id"`
	Digest   string                `json:"digest"`
	Revision int                   `json:"revision"`
	Graph    engineeringplan.Graph `json:"graph"`
}

// GraphProgress records one durable attempt with actual evidence IDs.
type GraphProgress struct {
	Version              int    `json:"version"`
	PlanID               string `json:"plan_id"`
	Digest               string `json:"digest"`
	TaskID               string `json:"task_id"`
	AttemptID            string `json:"attempt_id"`
	Outcome              string `json:"outcome"`
	ExplorerInvocationID string `json:"explorer_invocation_id,omitempty"`
	CandidateID          string `json:"candidate_id,omitempty"`
	WriterInvocationID   string `json:"writer_invocation_id,omitempty"`
	VerificationPlanID   string `json:"verification_plan_id,omitempty"`
	ReviewInvocationID   string `json:"review_invocation_id,omitempty"`
}

// GraphRevision persists a validated plan evolution (e.g. scoped repair nodes).
type GraphRevision struct {
	Version    int                   `json:"version"`
	PlanID     string                `json:"plan_id"`
	PrevDigest string                `json:"prev_digest"`
	Digest     string                `json:"digest"`
	Revision   int                   `json:"revision"`
	Graph      engineeringplan.Graph `json:"graph"`
}

func graphEnabled(s Snapshot) bool {
	return s.Creation.Execution.GraphEnabled()
}

func requireGraphEnabled(s Snapshot) error {
	if !graphEnabled(s) {
		return errors.New("graph execution not enabled for this run")
	}
	return nil
}

func parseAcceptedGraph(s Snapshot) (engineeringplan.Graph, error) {
	if s.Plan == nil {
		return engineeringplan.Graph{}, errors.New("graph requires an accepted plan")
	}
	g, err := engineeringplan.ParsePlannerGraph([]byte(s.Plan.Output))
	if err != nil {
		return engineeringplan.Graph{}, rejectedSemanticOutput(err)
	}
	if len(g.Couplings) != 0 {
		if s.Creation.Execution == nil || s.Creation.Execution.IsolatedImplementationVersion != 3 || (s.Creation.Execution.IsolationCohortSelectorVersion != 2 && s.Creation.Execution.IsolationCohortSelectorVersion != 3) || s.Creation.Config.PlannerContract != plannerContractGraphV9 {
			return engineeringplan.Graph{}, rejectedSemanticOutput(errors.New("typed couplings require plan-graph-v9 with the coupling-aware staged selector"))
		}
	}
	if err := validateAgentContextGraph(s, g); err != nil {
		return engineeringplan.Graph{}, rejectedSemanticOutput(err)
	}
	maxInitialImplementations := 1
	if s.Creation.Execution != nil && s.Creation.Execution.ParallelImplementationVersion == 1 {
		maxInitialImplementations = 2
	} else if s.Creation.Execution != nil && (s.Creation.Execution.IsolatedImplementationVersion == 1 || s.Creation.Execution.IsolatedImplementationVersion == 2 || s.Creation.Execution.IsolatedImplementationVersion == 3) {
		maxInitialImplementations = 8
	}
	var validationErr error
	if s.Creation.Execution != nil && s.Creation.Execution.IsolatedImplementationVersion == 3 {
		validationErr = engineeringplan.ValidateAutonomousGraphWithStagedImplementations(g, maxInitialImplementations)
	} else if s.Creation.Execution != nil && (s.Creation.Execution.IsolatedImplementationVersion == 1 || s.Creation.Execution.IsolatedImplementationVersion == 2) {
		validationErr = engineeringplan.ValidateAutonomousGraphWithIsolatedImplementations(g, maxInitialImplementations)
	} else {
		validationErr = engineeringplan.ValidateAutonomousGraphWithImplementations(g, maxInitialImplementations)
	}
	if validationErr != nil {
		return engineeringplan.Graph{}, rejectedSemanticOutput(validationErr)
	}
	if repairPlanningEnabled(s) && s.Creation.Execution.MaxRepairs > 0 {
		scopeRoots := map[string]struct{}{}
		for _, task := range g.Tasks {
			if task.Kind != engineeringplan.Implementation {
				continue
			}
			for _, root := range task.ScopePaths {
				scopeRoots[root] = struct{}{}
			}
		}
		if len(scopeRoots) > 32 {
			return engineeringplan.Graph{}, errors.New("initial implementation scope union exceeds bounded repair design capacity of 32 paths")
		}
	}
	readTasks := 0
	for _, task := range g.Tasks {
		if task.Kind == engineeringplan.Research || task.Kind == engineeringplan.Design {
			readTasks++
		}
	}
	if readTasks > s.Creation.Config.MaxExplorationRecords() {
		return engineeringplan.Graph{}, errors.New("plan exceeds the configured research/design record budget")
	}
	if repairPlanningEnabled(s) && readTasks+s.Creation.Execution.MaxRepairs > s.Creation.Config.MaxExplorationRecords() {
		return engineeringplan.Graph{}, errors.New("plan leaves insufficient research/design records for bounded repair design")
	}
	repairNodesPerSlot := 3
	if repairPlanningEnabled(s) {
		repairNodesPerSlot = 4
	}
	if s.Creation.Execution != nil && len(g.Tasks)+repairNodesPerSlot*s.Creation.Execution.MaxRepairs > 64 {
		return engineeringplan.Graph{}, errors.New("plan leaves insufficient task capacity for its configured repair budget")
	}
	if g.Mode == engineeringplan.ModeDirect {
		impl := g.Tasks[0]
		verifyID, reviewID := "native-verification", "native-review"
		if impl.ID == verifyID || impl.ID == reviewID {
			verifyID += "-gate"
			reviewID += "-gate"
		}
		g.Mode = engineeringplan.ModeGraph
		g.Tasks = append(g.Tasks,
			engineeringplan.Task{ID: verifyID, Kind: engineeringplan.Verification, Title: "Verify the candidate", Dependencies: []string{impl.ID}, ScopePaths: impl.ScopePaths, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "test", Description: "native candidate verification"}}, EstimatedSeconds: 300},
			engineeringplan.Task{ID: reviewID, Kind: engineeringplan.Review, Title: "Review the verified candidate", Dependencies: []string{verifyID}, ScopePaths: impl.ScopePaths, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "review", Description: "native independent review"}}, EstimatedSeconds: 300})
		var directValidationErr error
		if s.Creation.Execution != nil && s.Creation.Execution.IsolatedImplementationVersion == 3 {
			directValidationErr = engineeringplan.ValidateAutonomousGraphWithStagedImplementations(g, maxInitialImplementations)
		} else if s.Creation.Execution != nil && (s.Creation.Execution.IsolatedImplementationVersion == 1 || s.Creation.Execution.IsolatedImplementationVersion == 2) {
			directValidationErr = engineeringplan.ValidateAutonomousGraphWithIsolatedImplementations(g, maxInitialImplementations)
		} else {
			directValidationErr = engineeringplan.ValidateAutonomousGraphWithImplementations(g, maxInitialImplementations)
		}
		if directValidationErr != nil {
			return engineeringplan.Graph{}, directValidationErr
		}
	}
	return g, nil
}

func replayGraph(s *Snapshot, e journal.Event) error {
	switch e.Kind {
	case "graph.recorded":
		var rec GraphRecord
		if err := canonical.Decode(e.Payload, &rec); err != nil {
			return err
		}
		if rec.Version != 1 || rec.Revision != 1 {
			return errors.New("invalid graph record version")
		}
		if s.Plan == nil || rec.PlanID != s.PlanID {
			return errors.New("graph plan substitution")
		}
		if err := safepath.RequireDigest(rec.PlanID); err != nil {
			return errors.New("invalid graph plan identity")
		}
		if err := safepath.RequireDigest(rec.Digest); err != nil {
			return errors.New("invalid graph digest")
		}
		if s.Graph != nil {
			if s.Graph.Digest == rec.Digest && s.Graph.PlanID == rec.PlanID {
				return nil
			}
			return errors.New("graph already recorded")
		}
		if s.State != "IMPLEMENTING" {
			return errors.New("graph record outside implementation")
		}
		expected, err := parseAcceptedGraph(*s)
		if err != nil {
			return err
		}
		expectedDigest, err := engineeringplan.Digest(expected)
		if err != nil || expectedDigest != rec.Digest {
			return errors.New("graph digest differs from accepted plan")
		}
		if !sameCanonicalGraph(expected, rec.Graph) {
			return errors.New("graph substitution")
		}
		s.Graph = &GraphState{PlanID: rec.PlanID, Digest: rec.Digest, Revision: 1, Graph: rec.Graph, Evidence: map[string]GraphTaskEvidence{}}
	case "graph.progress":
		var p GraphProgress
		if err := canonical.Decode(e.Payload, &p); err != nil {
			return err
		}
		if p.Version != 1 {
			return errors.New("invalid graph progress version")
		}
		if s.Graph == nil || p.PlanID != s.Graph.PlanID || p.PlanID != s.PlanID || p.Digest != s.Graph.Digest {
			return errors.New("graph progress plan or digest substitution")
		}
		if !graphAttemptIDPattern.MatchString(p.AttemptID) {
			return errors.New("invalid graph attempt identity")
		}
		if p.Outcome != "completed" && p.Outcome != "failed" && p.Outcome != "unknown" {
			return errors.New("invalid graph attempt outcome")
		}
		task, ok := s.Graph.Graph.Task(p.TaskID)
		if !ok {
			return errors.New("graph progress task missing")
		}
		// Actual dependency gating: all dependencies must be completed.
		byID := map[string]engineeringplan.Task{}
		for _, t := range s.Graph.Graph.Tasks {
			byID[t.ID] = t
		}
		for _, dep := range task.Dependencies {
			evidence, observed := s.Graph.Evidence[dep]
			if !byID[dep].Completed || !observed || evidence.Outcome != "completed" {
				return errors.New("graph dependency not completed")
			}
		}
		// Attempt history retained; duplicates rejected.
		for _, a := range task.Attempts {
			if a.ID == p.AttemptID {
				return errors.New("graph attempt already recorded")
			}
		}
		if len(task.Attempts) > 0 && task.Attempts[len(task.Attempts)-1].Outcome == engineeringplan.AttemptUnknown {
			return errors.New("graph task has unresolved attempt; retry denied")
		}
		if task.Completed {
			return errors.New("completed graph task cannot advance")
		}
		if err := validateGraphEvidence(*s, task, p); err != nil {
			return err
		}
		// Apply attempt.
		updated := s.Graph.Graph
		for i, t := range updated.Tasks {
			if t.ID != p.TaskID {
				continue
			}
			outcome := engineeringplan.AttemptOutcome(p.Outcome)
			updated.Tasks[i].Attempts = append(updated.Tasks[i].Attempts, engineeringplan.Attempt{ID: p.AttemptID, Outcome: outcome})
			if p.Outcome == "completed" {
				updated.Tasks[i].Completed = true
			}
		}
		if err := updated.Validate(); err != nil {
			return err
		}
		s.Graph.Graph = updated
		if s.Graph.Evidence == nil {
			s.Graph.Evidence = map[string]GraphTaskEvidence{}
		}
		s.Graph.Evidence[p.TaskID] = GraphTaskEvidence{
			TaskID: p.TaskID, AttemptID: p.AttemptID, Outcome: p.Outcome,
			ExplorerInvocationID: p.ExplorerInvocationID, CandidateID: p.CandidateID,
			WriterInvocationID: p.WriterInvocationID, VerificationPlanID: p.VerificationPlanID,
			ReviewInvocationID: p.ReviewInvocationID,
		}
	case "graph.revised":
		var rev GraphRevision
		if err := canonical.Decode(e.Payload, &rev); err != nil {
			return err
		}
		if rev.Version != 1 {
			return errors.New("invalid graph revision version")
		}
		if s.Graph == nil || rev.PlanID != s.Graph.PlanID || rev.PlanID != s.PlanID || rev.PrevDigest != s.Graph.Digest {
			return errors.New("graph revision plan or digest substitution")
		}
		if rev.Revision != s.Graph.Revision+1 {
			return errors.New("graph revision sequence mismatch")
		}
		if err := safepath.RequireDigest(rev.Digest); err != nil || err == nil && safepath.RequireDigest(rev.PrevDigest) != nil {
			return errors.New("invalid graph revision digest")
		}
		// UNKNOWN blocks any revision.
		for _, t := range s.Graph.Graph.Tasks {
			if len(t.Attempts) > 0 && t.Attempts[len(t.Attempts)-1].Outcome == engineeringplan.AttemptUnknown {
				return errors.New("graph has unresolved attempt; revision denied")
			}
		}
		if err := engineeringplan.ValidateAutonomousRevision(s.Graph.Graph, rev.Graph); err != nil {
			return err
		}
		if err := validateAgentContextGraph(*s, rev.Graph); err != nil {
			return err
		}
		if repairPlanningEnabled(*s) {
			if len(rev.Graph.Tasks) > len(s.Graph.Graph.Tasks) {
				if err := validateRepairDesignExtension(*s, rev.Graph); err != nil {
					return err
				}
			} else if err := validateRepairWriteRefinement(*s, rev.Graph); err != nil {
				return err
			}
		}
		digest, err := engineeringplan.Digest(rev.Graph)
		if err != nil || digest != rev.Digest {
			return errors.New("graph revision digest mismatch")
		}
		// Preserve completed evidence: completed tasks must retain evidence.
		for _, t := range s.Graph.Graph.Tasks {
			if t.Completed {
				if _, ok := s.Graph.Evidence[t.ID]; !ok {
					return errors.New("completed graph evidence missing")
				}
			}
		}
		s.Graph.Graph = rev.Graph
		s.Graph.Digest = rev.Digest
		s.Graph.Revision = rev.Revision
	default:
		return errors.New("unknown graph event")
	}
	return nil
}

func sameCanonicalGraph(a, b engineeringplan.Graph) bool {
	ab, err := canonical.Bytes(a)
	if err != nil {
		return false
	}
	bb, err := canonical.Bytes(b)
	if err != nil {
		return false
	}
	if len(ab) != len(bb) {
		return false
	}
	for i := range ab {
		if ab[i] != bb[i] {
			return false
		}
	}
	return true
}

func validateGraphEvidence(s Snapshot, task engineeringplan.Task, p GraphProgress) error {
	switch task.Kind {
	case engineeringplan.Research, engineeringplan.Design:
		if p.ExplorerInvocationID == "" {
			return errors.New("research task requires explorer evidence")
		}
		if err := safepath.RequireDigest(p.ExplorerInvocationID); err != nil {
			return errors.New("invalid explorer evidence identity")
		}
		found := false
		for _, rec := range s.Explorations {
			if rec.Invocation.ID == p.ExplorerInvocationID {
				found = true
				question, err := explorerQuestionForTask(s, task)
				if err != nil || rec.Question != question {
					return errors.New("explorer question does not bind graph task")
				}
				break
			}
		}
		if !found {
			return errors.New("explorer evidence not recorded")
		}
		if p.CandidateID != "" || p.WriterInvocationID != "" || p.VerificationPlanID != "" || p.ReviewInvocationID != "" {
			return errors.New("research evidence carries unrelated roles")
		}
	case engineeringplan.Implementation:
		if p.CandidateID == "" || p.WriterInvocationID == "" {
			return errors.New("implementation requires candidate and writer evidence")
		}
		if err := safepath.RequireDigest(p.CandidateID); err != nil {
			return errors.New("invalid candidate evidence")
		}
		if err := safepath.RequireDigest(p.WriterInvocationID); err != nil {
			return errors.New("invalid writer evidence")
		}
		if s.Candidate == nil {
			return errors.New("implementation candidate unavailable")
		}
		candidateID, err := s.Candidate.ID()
		if err != nil || candidateID != p.CandidateID {
			return errors.New("implementation candidate substitution")
		}
		if changes, scopeMember := scopeReplannedWriterChanges(s, task.ID, p.WriterInvocationID); scopeMember {
			if !graphWriterBatchEffectApplied(s) || !graphWriterChangesWithinTask(task, changes) {
				return errors.New("scope replanned writer evidence lacks its confirmed parent effect")
			}
			afterID, afterErr := s.GraphWriterBatch.Prepared.Proposal.After.ID()
			if afterErr != nil || afterID != p.CandidateID {
				return errors.New("scope replanned writer candidate binding mismatch")
			}
		} else if record, isBatchMember, validMember := graphWriterResultForTask(s, task.ID, p.WriterInvocationID); isBatchMember {
			if !validMember || !graphWriterBatchEffectApplied(s) {
				return errors.New("task-bound writer evidence not recorded")
			}
			afterID, aerr := s.GraphWriterBatch.Prepared.Proposal.After.ID()
			if aerr != nil || afterID != p.CandidateID || graphWriterRecordInvocation(record).ID != p.WriterInvocationID {
				return errors.New("aggregate writer candidate binding mismatch")
			}
		} else {
			if s.WriterProposal == nil || s.WriterProposal.Invocation.ID != p.WriterInvocationID {
				return errors.New("writer evidence not recorded")
			}
			afterID, aerr := s.WriterProposal.Prepared.Proposal.After.ID()
			if aerr != nil || afterID != p.CandidateID {
				return errors.New("writer candidate binding mismatch")
			}
		}
		// Native writer proposal paths must be within declared WritePaths.
		if err := requireWriterPathsInScope(task, s); err != nil {
			return err
		}
	case engineeringplan.Verification:
		if p.VerificationPlanID == "" {
			return errors.New("verification requires plan evidence")
		}
		if s.Verification == nil || s.Verification.PlanID != p.VerificationPlanID {
			return errors.New("verification evidence not recorded")
		}
		if p.Outcome == "completed" {
			if len(s.Verification.Observations) != len(s.Verification.Plan.Invocations) {
				return errors.New("verification incomplete")
			}
			for _, o := range s.Verification.Observations {
				if o.Result.Status != "PASS" {
					return errors.New("verification did not pass")
				}
			}
		}
	case engineeringplan.Review:
		if p.ReviewInvocationID == "" {
			return errors.New("review requires invocation evidence")
		}
		if s.Review == nil || s.Review.Invocation.ID != p.ReviewInvocationID {
			return errors.New("review evidence not recorded")
		}
		var verdict ReviewVerdict
		if err := canonical.Decode([]byte(s.Review.Result.Output), &verdict); err != nil {
			return err
		}
		if (verdict.Decision == "approve" && p.Outcome != "completed") || (verdict.Decision == "changes_requested" && p.Outcome != "failed") {
			return errors.New("graph review outcome contradicts recorded verdict")
		}
	default:
		return errors.New("unknown graph task kind")
	}
	return nil
}

func requireWriterPathsInScope(task engineeringplan.Task, s Snapshot) error {
	var changes []fileeffects.Change
	if retained, scopeMember := scopeReplannedWriterChanges(s, task.ID, ""); scopeMember {
		changes = retained
	} else if record, isBatchMember, validMember := graphWriterResultForTask(s, task.ID, ""); isBatchMember {
		if !validMember {
			return errors.New("task-bound writer proposal unavailable for scope check")
		}
		changes = graphWriterRecordChanges(record)
	} else {
		if s.WriterProposal == nil {
			return errors.New("writer proposal unavailable for scope check")
		}
		changes = s.WriterProposal.Prepared.Proposal.Changes
	}
	for _, c := range changes {
		ok := false
		for _, w := range task.WritePaths {
			if c.Path == w || strings.HasPrefix(c.Path, w+"/") {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("%w: writer path %q outside declared implementation scope", ErrScopeReplanRequired, c.Path)
		}
	}
	return nil
}

// graphWriterResultForTask recognizes only a task that is an exact member of
// the recorded initial aggregate. The policy alone does not make a later
// serial repair a member of that cohort.
func graphWriterResultForTask(s Snapshot, taskID, invocationID string) (GraphWriterRecord, bool, bool) {
	if s.GraphWriterBatch == nil || taskID == "" {
		return GraphWriterRecord{}, false, false
	}
	for _, member := range s.GraphWriterBatch.Members {
		if member.TaskID != taskID {
			continue
		}
		record, ok := s.GraphWriterResults[taskID]
		valid := ok && record.TaskID == taskID && graphWriterRecordInvocation(record).ID == member.InvocationID && (invocationID == "" || member.InvocationID == invocationID)
		return record, true, valid
	}
	return GraphWriterRecord{}, false, false
}

// requireGraphReady verifies mandatory graph tasks have actual evidence before READY.
func requireGraphReady(s Snapshot) error {
	if s.Graph == nil {
		return nil
	}
	for _, t := range s.Graph.Graph.Tasks {
		if graphTaskSuperseded(s, t) {
			continue
		}
		if !t.Completed {
			return fmt.Errorf("graph task %q not completed", t.ID)
		}
		ev, ok := s.Graph.Evidence[t.ID]
		if !ok || ev.AttemptID == "" || ev.Outcome != "completed" {
			return fmt.Errorf("graph task %q lacks completed evidence", t.ID)
		}
		// Cross-check evidence still present in controller state.
		switch t.Kind {
		case engineeringplan.Research, engineeringplan.Design:
			found := false
			for _, rec := range s.Explorations {
				if rec.Invocation.ID == ev.ExplorerInvocationID {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("graph task %q explorer evidence missing", t.ID)
			}
		case engineeringplan.Implementation:
			if s.Candidate == nil {
				return fmt.Errorf("graph task %q candidate missing", t.ID)
			}
		case engineeringplan.Verification:
			if isLatestGraphGate(s, t) && (s.Verification == nil || s.Verification.PlanID != ev.VerificationPlanID) {
				return fmt.Errorf("graph task %q verification evidence missing", t.ID)
			}
		case engineeringplan.Review:
			if isLatestGraphGate(s, t) && (s.Review == nil || s.Review.Invocation.ID != ev.ReviewInvocationID) {
				return fmt.Errorf("graph task %q review evidence missing", t.ID)
			}
		}
	}
	return nil
}

func graphReadyTasks(s Snapshot) ([]engineeringplan.Task, error) {
	if s.Graph == nil {
		return nil, errors.New("graph not recorded")
	}
	ready, err := engineeringplan.Ready(s.Graph.Graph)
	if err != nil {
		return nil, err
	}
	filtered := ready[:0]
	for _, task := range ready {
		if !graphTaskSuperseded(s, task) {
			filtered = append(filtered, task)
		}
	}
	return filtered, nil
}

func isLatestGraphGate(s Snapshot, task engineeringplan.Task) bool {
	for i := len(s.Graph.Graph.Tasks) - 1; i >= 0; i-- {
		if s.Graph.Graph.Tasks[i].Kind == task.Kind {
			return s.Graph.Graph.Tasks[i].ID == task.ID
		}
	}
	return false
}

func graphTaskSuperseded(s Snapshot, task engineeringplan.Task) bool {
	// A never-started review of a failed verification belongs to the old
	// candidate. Its explicit repair supplies fresh verification and review.
	if task.Kind == engineeringplan.Review && !task.Completed && len(task.Attempts) == 0 {
		for _, dependency := range task.Dependencies {
			parent, ok := s.Graph.Graph.Task(dependency)
			if ok && parent.Kind == engineeringplan.Verification && graphTaskSuperseded(s, parent) {
				return true
			}
		}
	}
	ev, ok := s.Graph.Evidence[task.ID]
	if !ok || ev.Outcome != "failed" {
		return false
	}
	for _, child := range s.Graph.Graph.Tasks {
		if child.Kind != engineeringplan.Implementation || child.ParentID != task.ID {
			continue
		}
		for _, expected := range child.ExpectedEvidence {
			if expected.Kind == "failure" && expected.Description != "" && (expected.Description == ev.AttemptID || expected.Description == ev.VerificationPlanID || expected.Description == ev.ReviewInvocationID) {
				return true
			}
		}
	}
	return false
}

func graphImplementationTask(s Snapshot) (engineeringplan.Task, bool) {
	if s.Graph == nil {
		return engineeringplan.Task{}, false
	}
	for _, t := range s.Graph.Graph.Tasks {
		if t.Kind == engineeringplan.Implementation && !t.Completed {
			// Return the first incomplete implementation (initial or repair).
			return t, true
		}
	}
	for _, t := range s.Graph.Graph.Tasks {
		if t.Kind == engineeringplan.Implementation {
			return t, true
		}
	}
	return engineeringplan.Task{}, false
}

// explorerQuestionForTask builds the bounded exact explorer question carrying
// graph task identity, title, scope and expected evidence (<=4096 bytes).
func explorerQuestionForTask(s Snapshot, task engineeringplan.Task) (string, error) {
	evidence := make([]string, 0, len(task.ExpectedEvidence))
	for _, e := range task.ExpectedEvidence {
		evidence = append(evidence, e.Kind+":"+e.Description)
	}
	scope := strings.Join(task.ScopePaths, ",")
	question := fmt.Sprintf("[%s] %s | scope: %s | evidence: %s | objective: %s", task.ID, task.Title, scope, strings.Join(evidence, ";"), s.Creation.Objective)
	for _, expected := range task.ExpectedEvidence {
		if repairPlanningEnabled(s) && task.Kind == engineeringplan.Design && expected.Kind == "repair-attempt" {
			if s.Candidate == nil {
				return "", errors.New("repair design requires an exact candidate")
			}
			candidateID, err := s.Candidate.ID()
			if err != nil {
				return "", err
			}
			question = fmt.Sprintf("[%s] REPAIR DESIGN: inspect the exact failed-gate evidence and candidate %s; return every concrete repository-relative file path needed for a repair, all within the original implementation scope. State uncertainties and rationale. | task: %s | scope: %s | evidence: %s | objective: %s", candidateID, candidateID, task.ID, scope, strings.Join(evidence, ";"), s.Creation.Objective)
			break
		}
	}
	if len(question) > 4096 {
		// Trim objective to fit the bound while preserving task identity.
		over := len(question) - 4096
		obj := s.Creation.Objective
		if over < len(obj) {
			obj = obj[:len(obj)-over]
		} else {
			obj = ""
		}
		question = fmt.Sprintf("[%s] %s | scope: %s | evidence: %s | objective: %s", task.ID, task.Title, scope, strings.Join(evidence, ";"), obj)
	}
	question = strings.TrimSpace(question)
	if question == "" || len(question) > 4096 {
		return "", errors.New("graph explorer question bound exceeded")
	}
	return explorerCapacityRetryQuestion(s, question)
}

// ensureGraphRecorded parses the accepted plan and persists revision 1.
func ensureGraphRecorded(path string, s Snapshot) (Snapshot, error) {
	if s.Graph != nil {
		return s, nil
	}
	g, err := parseAcceptedGraph(s)
	if err != nil {
		return s, err
	}
	digest, err := engineeringplan.Digest(g)
	if err != nil {
		return s, err
	}
	rec := GraphRecord{Version: 1, PlanID: s.PlanID, Digest: digest, Revision: 1, Graph: g}
	if err := Append(path, "graph.recorded", rec); err != nil {
		return InspectOr(s, path, err)
	}
	return Inspect(path)
}

func recordGraphProgress(path string, p GraphProgress) (Snapshot, error) {
	if err := Append(path, "graph.progress", p); err != nil {
		return InspectOr(Snapshot{}, path, err)
	}
	return Inspect(path)
}

func appendGraphRevision(path string, next engineeringplan.Graph) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if s.Graph == nil {
		return s, errors.New("graph revision without recorded graph")
	}
	digest, err := engineeringplan.Digest(next)
	if err != nil {
		return s, err
	}
	rev := GraphRevision{Version: 1, PlanID: s.Graph.PlanID, PrevDigest: s.Graph.Digest, Digest: digest, Revision: s.Graph.Revision + 1, Graph: next}
	if err := Append(path, "graph.revised", rev); err != nil {
		return InspectOr(s, path, err)
	}
	return Inspect(path)
}

// graphRepairForFailure appends scoped repair implementation/verification/review
// nodes carrying actual failed evidence, within the existing repair bound.
func graphRepairForFailure(path string, failedTaskID, failedEvidence string) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if s.Graph == nil {
		return s, errors.New("repair requires a recorded graph")
	}
	seq := s.RepairAttempts + 1
	var next engineeringplan.Graph
	if repairPlanningEnabled(s) {
		next, err = repairDesignExtensionForSnapshot(s, failedTaskID, failedEvidence, seq)
	} else {
		next, err = engineeringplan.RepairExtension(s.Graph.Graph, failedTaskID, failedEvidence, seq)
	}
	if err != nil {
		return s, err
	}
	return appendGraphRevision(path, next)
}

func graphHasUnknown(s Snapshot) bool {
	if s.Graph == nil {
		return false
	}
	for _, t := range s.Graph.Graph.Tasks {
		if len(t.Attempts) > 0 && t.Attempts[len(t.Attempts)-1].Outcome == engineeringplan.AttemptUnknown {
			return true
		}
	}
	return false
}

// buildExplorerBatch admits task_context for each exact question and freezes
// role RI/lexical selection before preparing static scheduler claims.
func buildExplorerBatch(ctx context.Context, controllerPath string, s Snapshot, tasks []engineeringplan.Task) ([]taskscheduler.TaskSpec, error) {
	if len(tasks) == 0 {
		return nil, errors.New("empty explorer batch")
	}
	// Freeze RI/lexical: capture current selections; any drift rejects.
	frozenRI, err := roleRI(s)
	if err != nil {
		return nil, err
	}
	frozenLex, err := roleLexical(s)
	if err != nil {
		return nil, err
	}
	var frozenRIBinding string
	if frozenRI != nil {
		raw, err := canonical.Bytes(frozenRI.Binding)
		if err != nil {
			return nil, err
		}
		frozenRIBinding = string(raw)
	}
	var frozenLexID string
	if frozenLex != nil {
		frozenLexID = frozenLex.BuildID + "|" + frozenLex.OverlayID
	}
	specs := make([]taskscheduler.TaskSpec, 0, len(tasks))
	for _, task := range tasks {
		question, err := explorerQuestionForTask(s, task)
		if err != nil {
			return nil, err
		}
		// Admit task_context for EACH exact question before scheduling.
		if taskContextEnabled(s) {
			admitted := false
			for attempt := 0; attempt < 5 && !admitted; attempt++ {
				if _, err := AdmitTaskContext(ctx, controllerPath, "explorer", question); err != nil {
					if errors.Is(err, journal.ErrLockUnavailable) {
						continue
					}
					// Reuse without new effects is ok; missing admission is fatal.
					latest, ierr := Inspect(controllerPath)
					if ierr != nil {
						return nil, err
					}
					s = latest
					if _, qerr := taskContextForRole(s, "explorer", question); qerr != nil {
						return nil, err
					}
					admitted = true
				} else {
					latest, ierr := Inspect(controllerPath)
					if ierr != nil {
						return nil, ierr
					}
					s = latest
					admitted = true
				}
			}
			if !admitted {
				return nil, errors.New("task context admission lock contention")
			}
		}
		// Confirm frozen RI/lexical unchanged after context admission.
		currentRI, err := roleRI(s)
		if err != nil {
			return nil, err
		}
		currentLex, err := roleLexical(s)
		if err != nil {
			return nil, err
		}
		var curRIBinding string
		if currentRI != nil {
			raw, err := canonical.Bytes(currentRI.Binding)
			if err != nil {
				return nil, err
			}
			curRIBinding = string(raw)
		}
		var curLexID string
		if currentLex != nil {
			curLexID = currentLex.BuildID + "|" + currentLex.OverlayID
		}
		if curRIBinding != frozenRIBinding || curLexID != frozenLexID {
			return nil, errors.New("frozen lexical/context drift before batch")
		}
		invocation, err := explorerInvocation(s, question)
		if err != nil {
			return nil, err
		}
		// explorerInvocation binds candidate/PlanID/objective/question/RI/lexical/context,
		// not prior explorations/head: verified by exact recomputation at dispatch.
		spec := taskscheduler.TaskSpec{
			ID:             task.ID,
			RunID:          s.RunID,
			ControllerPath: controllerPath,
			Operation:      taskscheduler.OperationExplorer,
			Input:          question,
			InvocationID:   invocation.ID,
		}
		specs = append(specs, spec)
	}
	// Deterministic order for durable binding.
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	return specs, nil
}

// isStaticGraphExplorerCohort reports whether a scheduler claim is the narrow
// static graph-bound explorer case that may tolerate sibling read-only
// journal appends. Any writer/reviewer/planner operation, dynamic turn, or
// non-graph run returns false so legacy and exclusive head gates remain.
func isStaticGraphExplorerCohort(s Snapshot, claim taskscheduler.Claim) bool {
	if claim.Task.Operation != taskscheduler.OperationExplorer || claim.AgentTurn != nil {
		return false
	}
	if !graphEnabled(s) || !v1StaticGraphExplorerStateAllowed(s) {
		return false
	}
	if s.Workspace == nil || s.Candidate == nil || s.Plan == nil || s.Graph == nil {
		return false
	}
	if claim.Task.RunID != s.RunID {
		return false
	}
	ready, err := graphReadyTasks(s)
	if err != nil {
		return false
	}
	for _, task := range ready {
		if task.ID != claim.Task.ID || (task.Kind != engineeringplan.Research && task.Kind != engineeringplan.Design) {
			continue
		}
		question, err := explorerQuestionForTask(s, task)
		if err != nil || question != claim.Task.Input {
			return false
		}
		invocation, err := explorerInvocation(s, question)
		return err == nil && invocation.ID == claim.Task.InvocationID
	}
	return false
}

// verifyGraphCohortDelta allows a stale ControllerHead only when every journal
// event since the bound head is attributable to exact cohort explorer
// invocations and read-only kinds. Writer, lifecycle, mutation, unrelated or
// UNKNOWN events reject. Candidate fingerprint, lifecycle, PlanID and host
// bindings must be unchanged; no concurrent writer is allowed.
func verifyGraphCohortDelta(controllerPath string, claim taskscheduler.Claim) error {
	events, err := journal.Read(controllerPath)
	if err != nil || len(events) == 0 {
		return errors.New("controller journal unavailable for cohort check")
	}
	bound := claim.ControllerHead
	idx := -1
	for i, e := range events {
		if e.Hash == bound {
			idx = i
		}
	}
	if idx < 0 {
		return errors.New("bound controller head not found")
	}
	delta := events[idx+1:]
	if len(delta) == 0 {
		return nil
	}
	// Replay bound snapshot for frozen comparison.
	boundSnap, err := Replay(events[:idx+1])
	if err != nil {
		return err
	}
	currentSnap, err := Replay(events)
	if err != nil {
		return err
	}
	if boundSnap.RunID != currentSnap.RunID || boundSnap.PlanID != currentSnap.PlanID {
		return errors.New("cohort run or plan changed")
	}
	if (boundSnap.Candidate == nil) != (currentSnap.Candidate == nil) {
		return errors.New("cohort candidate changed")
	}
	if boundSnap.Candidate != nil && currentSnap.Candidate != nil && *boundSnap.Candidate != *currentSnap.Candidate {
		return errors.New("cohort candidate changed during batch")
	}
	if lifecycleStatus(boundSnap) != lifecycleStatus(currentSnap) {
		return errors.New("cohort lifecycle changed")
	}
	if boundSnap.FileOutcome == "UNKNOWN" || currentSnap.FileOutcome == "UNKNOWN" {
		return errors.New("cohort has UNKNOWN file effect")
	}
	if graphHasUnknown(currentSnap) || graphHasUnknown(boundSnap) {
		return errors.New("cohort has UNKNOWN graph attempt")
	}
	cohortQuestions := map[string]bool{}
	cohortInvocations := map[string]string{}
	cohortTasks := map[string]string{}
	ready, err := graphReadyTasks(boundSnap)
	if err != nil {
		return err
	}
	for _, task := range ready {
		if task.Kind != engineeringplan.Research && task.Kind != engineeringplan.Design {
			continue
		}
		question, err := explorerQuestionForTask(boundSnap, task)
		if err != nil {
			return err
		}
		invocation, err := explorerInvocation(boundSnap, question)
		if err != nil {
			return err
		}
		cohortQuestions[question] = true
		cohortInvocations[invocation.ID] = question
		cohortTasks[task.ID] = invocation.ID
	}
	allowed := map[string]bool{
		"explorer.recorded":         true,
		"task.context-admitted":     true,
		"explorer.host-intent":      true,
		"explorer.host-ready":       true,
		"explorer.host-observed":    true,
		"explorer.runtime-observed": true,
		"graph.progress":            true,
	}
	for _, e := range delta {
		if !allowed[e.Kind] {
			return fmt.Errorf("cohort sibling event %q not read-only", e.Kind)
		}
		switch e.Kind {
		case "explorer.recorded":
			var rec ExplorerRecord
			if err := canonical.Decode(e.Payload, &rec); err != nil {
				return err
			}
			if rec.Invocation.ID == claim.Task.InvocationID {
				return errors.New("cohort duplicate invocation")
			}
			if cohortInvocations[rec.Invocation.ID] != rec.Question {
				return errors.New("explorer is outside frozen graph cohort")
			}
			// Attributable to exact cohort: recompute from frozen snapshot.
			want, err := explorerInvocation(boundSnap, rec.Question)
			if err != nil || want.ID != rec.Invocation.ID || want != rec.Invocation {
				return errors.New("cohort explorer invocation not attributable")
			}
			if rec.Invocation.Profile.Role != "explorer" {
				return errors.New("cohort explorer role mismatch")
			}
		case "task.context-admitted":
			var rec TaskContextRecord
			if err := canonical.Decode(e.Payload, &rec); err != nil {
				return err
			}
			if rec.Role != "explorer" || !cohortQuestions[rec.Query] {
				return errors.New("cohort context role mismatch")
			}
		case "explorer.host-intent":
			var intent ExplorerHostIntent
			if err := canonical.Decode(e.Payload, &intent); err != nil {
				return err
			}
			want, err := expectedExplorerHost(boundSnap, intent.Question)
			if err != nil || want != intent || !cohortQuestions[intent.Question] {
				return errors.New("cohort host intent not attributable")
			}
		case "explorer.host-ready":
			var intent ExplorerHostIntent
			if err := canonical.Decode(e.Payload, &intent); err != nil {
				return err
			}
			want, err := expectedExplorerHost(boundSnap, intent.Question)
			if err != nil || want != intent || !cohortQuestions[intent.Question] {
				return errors.New("cohort host ready not attributable")
			}
		case "explorer.runtime-observed":
			var receipt ExplorerRuntimeReceipt
			if err := canonical.Decode(e.Payload, &receipt); err != nil {
				return err
			}
			if _, ok := cohortInvocations[receipt.InvocationID]; !ok {
				return errors.New("runtime receipt is outside frozen graph cohort")
			}
		case "explorer.host-observed":
			var receipt codexhost.Receipt
			if err := canonical.Decode(e.Payload, &receipt); err != nil {
				return err
			}
			matched := false
			for invocationID := range cohortInvocations {
				host, ok := explorerRunForInvocation(currentSnap, invocationID)
				if ok && host.Receipt != nil && host.Receipt.LaunchID == receipt.LaunchID {
					matched = true
					break
				}
			}
			if !matched {
				return errors.New("host receipt is outside frozen graph cohort")
			}
		case "graph.progress":
			var p GraphProgress
			if err := canonical.Decode(e.Payload, &p); err != nil {
				return err
			}
			if p.Outcome == "unknown" {
				return errors.New("cohort UNKNOWN progress cannot be retried")
			}
			if p.ExplorerInvocationID == claim.Task.InvocationID {
				return errors.New("cohort progress duplicates claiming invocation")
			}
			if cohortTasks[p.TaskID] == "" || cohortTasks[p.TaskID] != p.ExplorerInvocationID {
				return errors.New("progress is outside frozen graph cohort")
			}
			// Only research/design progress (read-only) may interleave.
			task, ok := boundSnap.Graph.Graph.Task(p.TaskID)
			if !ok || (task.Kind != engineeringplan.Research && task.Kind != engineeringplan.Design) {
				return errors.New("cohort progress not read-only research")
			}
		}
	}
	// Exact recomputed invocation must still match the claim (candidate,
	// PlanID, RI/lexical/context unchanged apart from cohort siblings).
	fresh, _, freshInv, err := scheduledInvocation(claim.Task, claim.AgentTurn)
	if err != nil {
		return err
	}
	if fresh.RunID != claim.Task.RunID || freshInv.ID != claim.Task.InvocationID {
		return errors.New("cohort invocation identity changed")
	}
	_ = fresh
	return nil
}

// runAutonomousGraph advances a graph-enabled run through hierarchical task
// execution with bounded parallel read-only explorers. It reuses every
// controller/effect/runtime safeguard and the durable scheduler seams; it
// introduces no generic recovery or extra authorization.
func runAutonomousGraph(ctx context.Context, path string, prepareOnly bool) (result Snapshot, failure error) {
	defer func() {
		if failure != nil {
			failure = ClassifyAutonomousFailure(result, failure)
		}
	}()
	progress := autonomousProgress{}
	for {
		if err := ctx.Err(); err != nil {
			s, inspectErr := Inspect(path)
			if inspectErr != nil {
				return s, errors.Join(err, inspectErr)
			}
			return s, err
		}
		s, err := Inspect(path)
		if err != nil {
			return s, err
		}
		if _, err := autonomousPolicyID(s); err != nil {
			return s, err
		}
		if err := RequireDispatchAllowed(s); err != nil {
			return s, err
		}
		if err := progress.observe(s); err != nil {
			return s, err
		}
		switch s.State {
		case "OBJECTIVE", "PLANNING":
			if _, err := ResumePlanning(ctx, path); err != nil {
				return InspectOr(s, path, err)
			}
		case "AWAITING_APPROVAL":
			if s.Creation.Execution.SemanticCorrectionVersion == 1 {
				if _, graphErr := parseAcceptedGraph(s); semanticOutputFailureOnly(graphErr) {
					if err := startPlannerSemanticCorrection(path, s); err != nil {
						return InspectOr(s, path, err)
					}
					continue
				} else if graphErr != nil {
					return s, graphErr
				}
			}
			policyID, err := autonomousPolicyID(s)
			if err != nil {
				return s, err
			}
			if err := Append(path, "plan.autonomous-authorized", MachineApproval{PlanID: s.PlanID, PolicyID: policyID, Actor: "fabric:autonomous"}); err != nil {
				return InspectOr(s, path, err)
			}
		case "IMPLEMENTING":
			if prepareOnly {
				prepared, err := prepareAutonomousGraphBoundary(ctx, path, s)
				if err != nil {
					return prepared, err
				}
				return prepared, nil
			}
			next, done, err := autonomousGraphImplementing(ctx, path, s)
			if err != nil {
				return next, err
			}
			if done {
				continue
			}
		case "VERIFYING":
			if s.Verification != nil && s.Verification.Pending {
				return s, errors.New("verification is pending and cannot be resent automatically")
			}
			if err := autonomousDispatchBlocked(s); err != nil {
				return s, err
			}
			_, err := Verify(ctx, path)
			if err != nil {
				return InspectOr(s, path, err)
			}
			// Record verification gate progress with actual evidence.
			if err := recordGraphVerificationProgress(path); err != nil {
				return InspectOr(s, path, err)
			}
		case "REVIEWING":
			// Native verification may have completed before the process stopped
			// while recording its graph progress. Observe that same receipt.
			if err := recordGraphVerificationProgress(path); err != nil {
				return InspectOr(s, path, err)
			}
			if err := autonomousDispatchBlocked(s); err != nil {
				return s, err
			}
			reviewed, err := RunReview(ctx, path)
			if err != nil {
				return InspectOr(s, path, err)
			}
			_ = reviewed
			if err := recordGraphReviewProgress(path); err != nil {
				return InspectOr(s, path, err)
			}
			// REPAIRING owns graph evolution and the corresponding repair slot.
		case "REPAIRING":
			next, err := autonomousGraphRepairing(ctx, path, s)
			if err != nil {
				return next, err
			}
			_ = next
		case "READY":
			if err := recordGraphReviewProgress(path); err != nil {
				return InspectOr(s, path, err)
			}
			latest, err := Inspect(path)
			if err != nil {
				return s, err
			}
			if err := requireGraphReady(latest); err != nil {
				return latest, err
			}
			return latest, nil
		default:
			return s, fmt.Errorf("autonomous execution cannot advance state %q", s.State)
		}
	}
}

// prepareAutonomousGraphBoundary stops at the only safe lexical-staging seam:
// the exact accepted graph is recorded and the run owns a confirmed pristine
// candidate, while no explorer or writer attempt has started. It resolves an
// existing workspace intent by observation only and never starts model work.
func prepareAutonomousGraphBoundary(ctx context.Context, path string, s Snapshot) (Snapshot, error) {
	if s.State != "IMPLEMENTING" || !graphEnabled(s) {
		return s, errors.New("autonomous preparation requires an implementing graph run")
	}
	if err := requireFreshGraphPreparationState(s); err != nil {
		return s, err
	}
	if err := autonomousDispatchBlocked(s); err != nil {
		return s, err
	}
	var err error
	s, err = ensureGraphRecorded(path, s)
	if err != nil {
		return InspectOr(s, path, err)
	}
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	if s.Workspace == nil {
		if s.WorkspaceIntent != nil {
			if _, err := ReconcileWorkspace(ctx, path); err != nil {
				return InspectOr(s, path, fmt.Errorf("autonomous workspace remains unresolved: %w", err))
			}
		} else {
			if _, err := StartWorkspace(ctx, path); err != nil {
				return InspectOr(s, path, err)
			}
		}
	}
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	if s.Workspace == nil || s.Candidate == nil || s.WorkspaceOutcome != "CONFIRMED" {
		return s, errors.New("autonomous preparation requires a confirmed workspace candidate")
	}
	lease, err := worktree.Acquire(s.Workspace.Request)
	if err != nil {
		return s, err
	}
	latest, inspectErr := Inspect(path)
	if inspectErr == nil && (latest.Workspace == nil || latest.Candidate == nil || *latest.Workspace != *s.Workspace || *latest.Candidate != *s.Candidate) {
		inspectErr = errors.New("autonomous preparation workspace changed during lease acquisition")
	}
	if inspectErr == nil {
		fingerprint, fingerprintErr := worktree.Fingerprint(ctx, *latest.Workspace)
		if fingerprintErr != nil {
			inspectErr = fingerprintErr
		} else if fingerprint != *latest.Candidate {
			inspectErr = errors.New("autonomous preparation candidate differs from live workspace")
		}
	}
	if closeErr := lease.Close(); closeErr != nil {
		inspectErr = errors.Join(inspectErr, closeErr)
	}
	if inspectErr != nil {
		return s, inspectErr
	}
	s = latest
	if err := requireFreshGraphPreparationState(s); err != nil {
		return s, err
	}
	if s.Graph == nil || s.Graph.PlanID != s.PlanID || s.Graph.Revision != 1 {
		return s, errors.New("autonomous preparation requires revision 1 of the accepted graph")
	}
	accepted, err := parseAcceptedGraph(s)
	if err != nil {
		return s, err
	}
	digest, err := engineeringplan.Digest(accepted)
	if err != nil || digest != s.Graph.Digest || !sameCanonicalGraph(accepted, s.Graph.Graph) {
		return s, errors.New("autonomous preparation graph differs from accepted plan")
	}
	if err := prepareManagedExplorerRoot(path, s); err != nil {
		return s, err
	}
	return s, nil
}

// requireFreshGraphPreparationState prevents prepare-only from relabeling a
// partially executed or revised run as a fresh staging boundary.
func requireFreshGraphPreparationState(s Snapshot) error {
	if len(s.TaskContexts) != 0 {
		return errors.New("autonomous preparation boundary passed after task context was admitted")
	}
	if s.ExplorerHost != nil || len(s.ExplorerRuns) != 0 || len(s.Explorations) != 0 || s.WriterHost != nil || s.WriterProposal != nil || len(s.GraphWriterHosts) != 0 || len(s.GraphWriterResults) != 0 || s.GraphWriterBatch != nil || s.FileIntent != nil || s.Verification != nil || s.ReviewHost != nil || s.Review != nil {
		return errors.New("autonomous preparation boundary passed after role or file work started")
	}
	if s.Graph != nil {
		if s.Graph.Revision != 1 || len(s.Graph.Evidence) != 0 {
			return errors.New("autonomous preparation boundary passed after graph progress")
		}
		for _, task := range s.Graph.Graph.Tasks {
			if task.Completed || len(task.Attempts) != 0 {
				return errors.New("autonomous preparation boundary passed after graph task started")
			}
		}
	}
	return nil
}

func autonomousGraphImplementing(ctx context.Context, path string, s Snapshot) (Snapshot, bool, error) {
	if s.Workspace == nil {
		if s.WorkspaceIntent != nil {
			if _, err := ReconcileWorkspace(ctx, path); err != nil {
				return s, false, fmt.Errorf("autonomous workspace remains unresolved: %w", err)
			}
		} else {
			if err := autonomousDispatchBlocked(s); err != nil {
				return s, false, err
			}
			if _, err := StartWorkspace(ctx, path); err != nil {
				latest, ierr := InspectOr(s, path, err)
				return latest, false, ierr
			}
		}
		return s, true, nil
	}
	if s.FileOutcome == "UNKNOWN" {
		if _, err := ReconcileFiles(ctx, path); err != nil {
			return s, false, fmt.Errorf("autonomous file effect remains UNKNOWN: %w", err)
		}
		return s, true, nil
	}
	if err := prepareManagedExplorerRoot(path, s); err != nil {
		return s, false, err
	}
	if err := preflightAutonomousVerification(s); err != nil {
		return s, false, err
	}
	// Confirmed effect restarts at verification, recording implementation
	// progress first so READY requires actual evidence.
	if autonomousFilesApplied(s) {
		if err := autonomousDispatchBlocked(s); err != nil {
			return s, false, err
		}
		if err := recordGraphImplementationProgress(path); err != nil {
			latest, ierr := InspectOr(s, path, err)
			return latest, false, ierr
		}
		if stagedIsolationEnabled(s) {
			latest, err := Inspect(path)
			if err != nil {
				return s, false, err
			}
			if !stagedAllImplementationsCompleted(latest) {
				if err := AdvanceStagedCohort(path); err != nil {
					latest, ierr := InspectOr(s, path, err)
					return latest, false, ierr
				}
				return s, true, nil
			}
		}
		if _, err := Verify(ctx, path); err != nil {
			latest, ierr := InspectOr(s, path, err)
			return latest, false, ierr
		}
		if err := recordGraphVerificationProgress(path); err != nil {
			latest, ierr := InspectOr(s, path, err)
			return latest, false, ierr
		}
		return s, true, nil
	}
	var err error
	s, err = ensureGraphRecorded(path, s)
	if err != nil {
		latest, ierr := InspectOr(s, path, err)
		return latest, false, ierr
	}
	ready, err := graphReadyTasks(s)
	if err != nil {
		return s, false, err
	}
	var research []engineeringplan.Task
	var implReady *engineeringplan.Task
	var implReadyTasks []engineeringplan.Task
	for _, t := range ready {
		switch t.Kind {
		case engineeringplan.Research, engineeringplan.Design:
			research = append(research, t)
		case engineeringplan.Implementation:
			implReadyTasks = append(implReadyTasks, t)
			if implReady == nil {
				copy := t
				implReady = &copy
			}
		default:
			// Verification/review gates run in their native states.
		}
	}
	if len(research) > 0 {
		if err := autonomousDispatchBlocked(s); err != nil {
			return s, false, err
		}
		if err := runGraphResearchBatch(ctx, path, s, research); err != nil {
			latest, ierr := InspectOr(s, path, err)
			return latest, false, ierr
		}
		return s, true, nil
	}
	if implReady != nil {
		if repairPlanningEnabled(s) && len(implReady.WritePaths) == 0 {
			if err := refineRepairWritePathsFromDesign(path, s, *implReady); err != nil {
				return s, false, err
			}
			return s, true, nil
		}
		// Only a single writer after all required research/design tasks:
		// Ready already guarantees dependencies completed; additionally
		// require no incomplete research/design remains.
		for _, t := range s.Graph.Graph.Tasks {
			if (t.Kind == engineeringplan.Research || t.Kind == engineeringplan.Design) && !t.Completed {
				return s, false, fmt.Errorf("research task %q must complete before writer", t.ID)
			}
		}
		if stagedIsolationEnabled(s) {
			if _, err := stagedGraphWriterIDs(s, implReadyTasks); err != nil {
				return s, false, err
			}
			if err := autonomousDispatchBlocked(s); err != nil {
				return s, false, err
			}
			if s.GraphWriterBatch == nil {
				allRecorded := true
				for _, task := range implReadyTasks {
					record, ok := s.GraphWriterResults[task.ID]
					if !ok || record.Isolated == nil {
						allRecorded = false
						break
					}
				}
				if !allRecorded {
					if err := runStagedWaves(ctx, path, s, implReadyTasks); err != nil {
						latest, ierr := InspectOr(s, path, err)
						return latest, false, ierr
					}
					s, err = Inspect(path)
					if err != nil {
						return s, false, err
					}
				}
				if s.GraphWriterBatch != nil {
					return s, true, nil
				}
				batch, err := buildStagedBatch(ctx, path, taskIDs(implReadyTasks))
				if err != nil {
					return s, false, err
				}
				if err := recordGraphWriterBatch(path, batch); err != nil {
					latest, ierr := InspectOr(s, path, err)
					return latest, false, ierr
				}
				return s, true, nil
			}
			if _, err := applyStagedBatch(ctx, path, s); err != nil {
				latest, ierr := InspectOr(s, path, err)
				return latest, false, ierr
			}
			return s, true, nil
		}
		if isolatedImplementationEnabled(s) {
			if s.GraphWriterBatch == nil || s.GraphWriterBatch.Version != 3 {
				if _, err := isolatedGraphWriterIDs(s, implReadyTasks); err != nil {
					return s, false, err
				}
			}
			if err := autonomousDispatchBlocked(s); err != nil {
				return s, false, err
			}
			if s.GraphWriterBatch == nil {
				allRecorded := true
				for _, task := range implReadyTasks {
					record, ok := s.GraphWriterResults[task.ID]
					if !ok || record.Isolated == nil {
						allRecorded = false
						break
					}
				}
				if !allRecorded {
					if err := runIsolatedGraphWriterBatch(ctx, path, s, implReadyTasks); err != nil {
						latest, ierr := InspectOr(s, path, err)
						return latest, false, ierr
					}
					s, err = Inspect(path)
					if err != nil {
						return s, false, err
					}
				}
				if s.GraphWriterBatch != nil {
					return s, true, nil
				}
				batch, err := buildIsolatedGraphWriterBatch(ctx, path, taskIDs(implReadyTasks))
				if err != nil {
					return s, false, err
				}
				if err := recordGraphWriterBatch(path, batch); err != nil {
					latest, ierr := InspectOr(s, path, err)
					return latest, false, ierr
				}
				return s, true, nil
			}
			if _, err := applyGraphWriterBatch(ctx, path, s); err != nil {
				latest, ierr := InspectOr(s, path, err)
				return latest, false, ierr
			}
			return s, true, nil
		}
		if parallelImplementationEnabled(s) {
			if len(implReadyTasks) < 1 || len(implReadyTasks) > 2 {
				return s, false, errors.New("parallel implementation cohort must contain one or two ready tasks")
			}
			for i := 0; i < len(implReadyTasks); i++ {
				for j := i + 1; j < len(implReadyTasks); j++ {
					if !tasksIndependentAndDisjoint(implReadyTasks[i], implReadyTasks[j]) {
						return s, false, errors.New("parallel implementation tasks are not independent and disjoint")
					}
				}
			}
			if err := autonomousDispatchBlocked(s); err != nil {
				return s, false, err
			}
			if s.GraphWriterBatch == nil {
				allRecorded := true
				for _, task := range implReadyTasks {
					if _, ok := s.GraphWriterResults[task.ID]; !ok {
						allRecorded = false
						break
					}
				}
				if !allRecorded {
					if err := runGraphWriterBatch(ctx, path, s, implReadyTasks); err != nil {
						latest, ierr := InspectOr(s, path, err)
						return latest, false, ierr
					}
					s, err = Inspect(path)
					if err != nil {
						return s, false, err
					}
				}
				if s.GraphWriterBatch != nil {
					return s, true, nil
				}
				batch, err := buildGraphWriterBatch(s, taskIDs(implReadyTasks))
				if err != nil {
					return s, false, err
				}
				if err := recordGraphWriterBatch(path, batch); err != nil {
					latest, ierr := InspectOr(s, path, err)
					return latest, false, ierr
				}
				return s, true, nil
			}
			if _, err := applyGraphWriterBatch(ctx, path, s); err != nil {
				latest, ierr := InspectOr(s, path, err)
				return latest, false, ierr
			}
			return s, true, nil
		}
		// Opt-in automatic finite acquisition runs once before the initial
		// serial writer. It never dispatches a model and never blocks ordinary
		// mandatory writer context on no-positive outcomes.
		if s.Creation.Execution != nil && s.Creation.Execution.EvidencePolicy != nil {
			beforeHash := ""
			if h, herr := evidencePolicyExpectedHash(s); herr == nil {
				if hasEvidencePolicyDecision(s, h) {
					beforeHash = h
				}
			}
			updated, policyErr := maybeAcquireEvidencePolicy(ctx, path, s)
			if policyErr != nil {
				latest, ierr := InspectOr(s, path, policyErr)
				return latest, false, ierr
			}
			s = updated
			if beforeHash == "" {
				if h, herr := evidencePolicyExpectedHash(s); herr == nil && hasEvidencePolicyDecision(s, h) {
					return s, true, nil
				}
			}
		}
		if err := autonomousDispatchBlocked(s); err != nil {
			return s, false, err
		}
		ns, staged, err := autonomousWriterEffect(ctx, path, s)
		if err != nil {
			return ns, false, err
		}
		if staged {
			return ns, true, nil
		}
		// Writer applied: record implementation progress with fresh candidate.
		if err := recordGraphImplementationProgress(path); err != nil {
			latest, ierr := InspectOr(s, path, err)
			return latest, false, ierr
		}
		return s, true, nil
	}
	// No research/implementation ready: either verification/review gates are
	// pending in their native states, or the graph is blocked. If the writer
	// already applied (handled above) verification will advance; otherwise
	// surface dependency gating without fictional completion.
	if err := autonomousDispatchBlocked(s); err != nil {
		return s, false, err
	}
	if _, err := Verify(ctx, path); err != nil {
		latest, ierr := InspectOr(s, path, err)
		return latest, false, ierr
	}
	if err := recordGraphVerificationProgress(path); err != nil {
		latest, ierr := InspectOr(s, path, err)
		return latest, false, ierr
	}
	return s, true, nil
}

func autonomousGraphRepairing(ctx context.Context, path string, s Snapshot) (Snapshot, error) {
	if s.Candidate == nil {
		return s, errors.New("repair requires a candidate")
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return s, err
	}
	if s.FileOutcome == "UNKNOWN" {
		if _, err := ReconcileFiles(ctx, path); err != nil {
			return InspectOr(s, path, fmt.Errorf("autonomous file effect remains UNKNOWN: %w", err))
		}
		latest, _ := Inspect(path)
		return latest, nil
	}
	if err := rejectUnstartedAutonomousVerification(s); err != nil {
		return s, err
	}
	if autonomousRepairApplied(s) {
		if candidateID == s.RepairCandidateID {
			return s, errors.New("autonomous repair made no candidate progress")
		}
		// A confirmed writer effect may have been persisted just before a
		// restart, before its graph implementation progress. Record that exact
		// proposal/candidate before attempting the dependent native gate.
		if err := recordGraphImplementationProgress(path); err != nil {
			return InspectOr(s, path, err)
		}
		s, err = Inspect(path)
		if err != nil {
			return s, err
		}
		if !autonomousVerificationCovered(s, candidateID) {
			if err := autonomousDispatchBlocked(s); err != nil {
				return s, err
			}
			if _, err := Verify(ctx, path); err != nil {
				return InspectOr(s, path, err)
			}
			if err := recordGraphVerificationProgress(path); err != nil {
				return InspectOr(s, path, err)
			}
			latest, _ := Inspect(path)
			return latest, nil
		}
	}
	if s.RepairCandidateID != "" && s.RepairCandidateID == candidateID && s.FileOutcome == "NOT_APPLIED" {
		return s, errors.New("autonomous repair made no candidate progress")
	}
	if s.RepairCandidateID != candidateID {
		// Persist an already completed failed native gate before evolving a
		// repair. This observes existing evidence and never invokes a provider.
		if s.Review != nil {
			var verdict ReviewVerdict
			if err := canonical.Decode([]byte(s.Review.Result.Output), &verdict); err == nil && verdict.Decision == "changes_requested" && verdict.CandidateID == candidateID {
				if err := recordGraphReviewProgress(path); err != nil {
					return InspectOr(s, path, err)
				}
			} else if err := recordGraphVerificationProgress(path); err != nil {
				return InspectOr(s, path, err)
			}
		} else if err := recordGraphVerificationProgress(path); err != nil {
			return InspectOr(s, path, err)
		}
		var inspectErr error
		s, inspectErr = Inspect(path)
		if inspectErr != nil {
			return s, inspectErr
		}
		if s.RepairAttempts >= s.Creation.Execution.MaxRepairs {
			return s, fmt.Errorf("%w: autonomous repair bound reached (%d)", ErrAutonomousRepairBudget, s.RepairAttempts)
		}
		// Allow useful plan evolution before consuming the slot when the
		// failure carries failed verification/review evidence.
		if err := maybeEvolveGraphForRepair(path, s); err != nil {
			return s, err
		}
		if err := Append(path, "autonomous.repair-started", AutonomousRepair{Attempt: s.RepairAttempts + 1, CandidateID: candidateID}); err != nil {
			return InspectOr(s, path, err)
		}
		latest, _ := Inspect(path)
		return latest, nil
	}
	if repairPlanningEnabled(s) {
		ready, err := graphReadyTasks(s)
		if err != nil {
			return s, err
		}
		var designTasks []engineeringplan.Task
		for _, task := range ready {
			if task.Kind == engineeringplan.Research || task.Kind == engineeringplan.Design {
				designTasks = append(designTasks, task)
			}
		}
		if len(designTasks) > 0 {
			if err := autonomousDispatchBlocked(s); err != nil {
				return s, err
			}
			if err := runGraphResearchBatch(ctx, path, s, designTasks); err != nil {
				return InspectOr(s, path, err)
			}
			latest, err := Inspect(path)
			return latest, err
		}
		for _, task := range ready {
			if task.Kind == engineeringplan.Implementation && task.ParentID != "" && len(task.WritePaths) == 0 {
				if err := refineRepairWritePathsFromDesign(path, s, task); err != nil {
					return s, err
				}
				latest, err := Inspect(path)
				return latest, err
			}
		}
	}
	if ns, staged, err := autonomousWriterEffect(ctx, path, s); err != nil {
		return ns, err
	} else if staged {
		return ns, nil
	} else {
		_ = ns
	}
	if err := recordGraphImplementationProgress(path); err != nil {
		return InspectOr(s, path, err)
	}
	latest, _ := Inspect(path)
	return latest, nil
}

// recordGraphImplementationProgress binds the actual writer/candidate evidence.
func recordGraphImplementationProgress(path string) error {
	s, err := Inspect(path)
	if err != nil {
		return err
	}
	if s.GraphWriterBatch != nil && s.GraphWriterBatch.Version == 4 {
		return recordStagedImplementationProgress(path, s)
	}
	if graphWriterBatchEffectApplied(s) {
		return recordParallelGraphImplementationProgress(path, s)
	}
	if s.Graph == nil || s.Candidate == nil || s.WriterProposal == nil {
		return errors.New("implementation progress requires graph, candidate and writer evidence")
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return err
	}
	for _, task := range s.Graph.Graph.Tasks {
		ev, ok := s.Graph.Evidence[task.ID]
		if task.Kind == engineeringplan.Implementation && task.Completed && ok && ev.Outcome == "completed" && ev.CandidateID == candidateID && ev.WriterInvocationID == s.WriterProposal.Invocation.ID {
			return nil
		}
	}
	// Find the active (incomplete) implementation whose WritePaths admit the proposal.
	var active *engineeringplan.Task
	for _, t := range s.Graph.Graph.Tasks {
		if t.Kind != engineeringplan.Implementation || t.Completed {
			continue
		}
		ready := true
		byID := map[string]engineeringplan.Task{}
		for _, x := range s.Graph.Graph.Tasks {
			byID[x.ID] = x
		}
		for _, d := range t.Dependencies {
			if !byID[d].Completed {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		if requireWriterPathsInScope(t, s) == nil {
			copy := t
			active = &copy
			break
		}
	}
	if active == nil {
		return errors.New("no active implementation admits the writer proposal")
	}
	// Idempotent: already recorded.
	if ev, ok := s.Graph.Evidence[active.ID]; ok && ev.CandidateID == candidateID && ev.Outcome == "completed" {
		return nil
	}
	attemptID := fmt.Sprintf("attempt-%d", len(active.Attempts)+1)
	if !graphAttemptIDPattern.MatchString(attemptID) {
		attemptID = "attempt-x"
	}
	p := GraphProgress{Version: 1, PlanID: s.Graph.PlanID, Digest: s.Graph.Digest, TaskID: active.ID, AttemptID: attemptID, Outcome: "completed", CandidateID: candidateID, WriterInvocationID: s.WriterProposal.Invocation.ID}
	_, err = recordGraphProgress(path, p)
	return err
}

func taskIDs(tasks []engineeringplan.Task) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

func recordParallelGraphImplementationProgress(path string, s Snapshot) error {
	if s.Graph == nil || s.Candidate == nil || s.GraphWriterBatch == nil || s.FileOutcome != "CONFIRMED" || s.FileIntent == nil || s.FileIntent.Prepared.Intent != s.GraphWriterBatch.Prepared.Intent {
		return errors.New("parallel implementation progress requires its confirmed aggregate effect")
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return err
	}
	afterID, err := s.GraphWriterBatch.Prepared.Proposal.After.ID()
	if err != nil || afterID != candidateID {
		return errors.Join(errors.New("parallel writer aggregate candidate differs"), err)
	}
	for _, member := range s.GraphWriterBatch.Members {
		latest, task, done, err := inspectUnrecordedWriterMember(path, member, candidateID)
		if err != nil {
			return err
		}
		if done {
			continue
		}
		if task.ID == "" {
			return errors.New("parallel writer task disappeared from graph")
		}
		if changes, scopeMember := scopeReplannedWriterChanges(latest, member.TaskID, member.InvocationID); scopeMember {
			if !graphWriterChangesWithinTask(task, changes) {
				return errors.New("scope replanned writer changes escape admitted ownership")
			}
		} else {
			record, ok := latest.GraphWriterResults[member.TaskID]
			if !ok || graphWriterRecordInvocation(record).ID != member.InvocationID || !graphWriterChangesWithinTask(task, graphWriterRecordChanges(record)) {
				return errors.New("parallel writer task evidence is missing or substituted")
			}
		}
		attemptID := fmt.Sprintf("attempt-%d", len(task.Attempts)+1)
		p := GraphProgress{Version: 1, PlanID: latest.Graph.PlanID, Digest: latest.Graph.Digest, TaskID: task.ID, AttemptID: attemptID, Outcome: "completed", CandidateID: candidateID, WriterInvocationID: member.InvocationID}
		if _, err := recordGraphProgress(path, p); err != nil {
			return err
		}
	}
	return nil
}

func applyGraphWriterBatch(ctx context.Context, path string, s Snapshot) (Snapshot, error) {
	if s.GraphWriterBatch == nil || !graphWriterCohortEnabled(s) {
		return s, errors.New("parallel writer aggregate is unavailable")
	}
	if s.FileOutcome == "UNKNOWN" {
		return s, errors.New("parallel writer file effect is UNKNOWN")
	}
	if s.FileIntent != nil {
		return s, errors.New("parallel writer aggregate already has a file attempt")
	}
	return applyWriterBatchEffect(ctx, path, s)
}

func recordGraphVerificationProgress(path string) error {
	s, err := Inspect(path)
	if err != nil {
		return err
	}
	if s.Graph == nil || s.Verification == nil {
		return errors.New("verification progress requires graph and verification evidence")
	}
	for _, evidence := range s.Graph.Evidence {
		if evidence.VerificationPlanID == s.Verification.PlanID && (evidence.Outcome == "completed" || evidence.Outcome == "failed") {
			return nil
		}
	}
	ready, err := graphReadyTasks(s)
	if err != nil {
		return err
	}
	recorded := false
	for _, t := range ready {
		if t.Kind != engineeringplan.Verification {
			continue
		}
		outcome := "completed"
		if len(s.Verification.Observations) != len(s.Verification.Plan.Invocations) {
			continue
		}
		for _, o := range s.Verification.Observations {
			if o.Result.Status != "PASS" {
				outcome = "failed"
				break
			}
		}
		// If already recorded with same plan, skip.
		if ev, ok := s.Graph.Evidence[t.ID]; ok && ev.VerificationPlanID == s.Verification.PlanID {
			recorded = true
			continue
		}
		attemptID := fmt.Sprintf("attempt-%d", len(t.Attempts)+1)
		p := GraphProgress{Version: 1, PlanID: s.Graph.PlanID, Digest: s.Graph.Digest, TaskID: t.ID, AttemptID: attemptID, Outcome: outcome, VerificationPlanID: s.Verification.PlanID}
		if _, err := recordGraphProgress(path, p); err != nil {
			return err
		}
		recorded = true
	}
	// Also handle failed verification that is no longer ready because its
	// dependencies completed but it already has a failed attempt and needs a
	// repair extension: record failure for completed-dependency verification
	// tasks that lack evidence.
	if !recorded {
		for _, t := range s.Graph.Graph.Tasks {
			if t.Kind != engineeringplan.Verification || t.Completed {
				continue
			}
			if _, ok := s.Graph.Evidence[t.ID]; ok {
				continue
			}
			byID := map[string]engineeringplan.Task{}
			for _, x := range s.Graph.Graph.Tasks {
				byID[x.ID] = x
			}
			depsDone := true
			for _, d := range t.Dependencies {
				if !byID[d].Completed {
					depsDone = false
					break
				}
			}
			if !depsDone {
				continue
			}
			outcome := "completed"
			for _, o := range s.Verification.Observations {
				if o.Result.Status != "PASS" {
					outcome = "failed"
					break
				}
			}
			attemptID := fmt.Sprintf("attempt-%d", len(t.Attempts)+1)
			p := GraphProgress{Version: 1, PlanID: s.Graph.PlanID, Digest: s.Graph.Digest, TaskID: t.ID, AttemptID: attemptID, Outcome: outcome, VerificationPlanID: s.Verification.PlanID}
			if _, err := recordGraphProgress(path, p); err != nil {
				return err
			}
			recorded = true
			break
		}
	}
	if !recorded {
		return errors.New("no verification gate ready for progress")
	}
	return nil
}

func recordGraphReviewProgress(path string) error {
	s, err := Inspect(path)
	if err != nil {
		return err
	}
	if s.Graph == nil || s.Review == nil {
		return errors.New("review progress requires graph and review evidence")
	}
	for _, evidence := range s.Graph.Evidence {
		if evidence.ReviewInvocationID == s.Review.Invocation.ID && (evidence.Outcome == "completed" || evidence.Outcome == "failed") {
			return nil
		}
	}
	// Review verdict drives outcome: approve -> completed, else failed.
	var verdict ReviewVerdict
	// Decode via canonical to tolerate exact shape.
	raw := []byte(s.Review.Result.Output)
	if err := canonical.Decode(raw, &verdict); err != nil {
		return err
	}
	outcome := "completed"
	if verdict.Decision != "approve" {
		outcome = "failed"
	}
	ready, err := graphReadyTasks(s)
	if err != nil {
		return err
	}
	for _, t := range ready {
		if t.Kind != engineeringplan.Review {
			continue
		}
		if ev, ok := s.Graph.Evidence[t.ID]; ok && ev.ReviewInvocationID == s.Review.Invocation.ID {
			return nil
		}
		attemptID := fmt.Sprintf("attempt-%d", len(t.Attempts)+1)
		p := GraphProgress{Version: 1, PlanID: s.Graph.PlanID, Digest: s.Graph.Digest, TaskID: t.ID, AttemptID: attemptID, Outcome: outcome, ReviewInvocationID: s.Review.Invocation.ID}
		if _, err := recordGraphProgress(path, p); err != nil {
			return err
		}
		return nil
	}
	// Fallback: first incomplete review whose dependencies are done.
	for _, t := range s.Graph.Graph.Tasks {
		if t.Kind != engineeringplan.Review || t.Completed {
			continue
		}
		if _, ok := s.Graph.Evidence[t.ID]; ok {
			continue
		}
		attemptID := fmt.Sprintf("attempt-%d", len(t.Attempts)+1)
		p := GraphProgress{Version: 1, PlanID: s.Graph.PlanID, Digest: s.Graph.Digest, TaskID: t.ID, AttemptID: attemptID, Outcome: outcome, ReviewInvocationID: s.Review.Invocation.ID}
		if _, err := recordGraphProgress(path, p); err != nil {
			return err
		}
		return nil
	}
	return errors.New("no review gate ready for progress")
}

// maybeEvolveGraphForRepair appends scoped repair nodes when verification or
// review failed with actual evidence, preserving completed history.
func maybeEvolveGraphForRepair(path string, s Snapshot) error {
	if s.Graph == nil {
		return errors.New("repair evolution requires a graph")
	}
	// Find a failed verification/review task with evidence but no repair child yet.
	for index := len(s.Graph.Graph.Tasks) - 1; index >= 0; index-- {
		t := s.Graph.Graph.Tasks[index]
		ev, ok := s.Graph.Evidence[t.ID]
		if !ok || ev.Outcome != "failed" {
			continue
		}
		if t.Kind != engineeringplan.Verification && t.Kind != engineeringplan.Review {
			continue
		}
		// Already has a repair child?
		hasChild := false
		for _, other := range s.Graph.Graph.Tasks {
			if other.Kind != engineeringplan.Implementation || other.ParentID != t.ID {
				continue
			}
			for _, expected := range other.ExpectedEvidence {
				if expected.Kind == "failure" && expected.Description != "" && (expected.Description == ev.AttemptID || expected.Description == ev.VerificationPlanID || expected.Description == ev.ReviewInvocationID) {
					hasChild = true
					pendingSlot := false
					for _, identity := range other.ExpectedEvidence {
						if identity.Kind == "repair-attempt" && identity.Description == fmt.Sprintf("%d", s.RepairAttempts+1) {
							pendingSlot = true
						}
					}
					if pendingSlot && !other.Completed && len(other.Attempts) == 0 {
						// The revision was persisted before repair-started. Resume
						// the same slot instead of appending another revision.
						return nil
					}
					break
				}
			}
		}
		if hasChild {
			continue
		}
		failedEvidence := ev.AttemptID
		if ev.VerificationPlanID != "" {
			failedEvidence = ev.VerificationPlanID
		}
		if ev.ReviewInvocationID != "" {
			failedEvidence = ev.ReviewInvocationID
		}
		var next engineeringplan.Graph
		var err error
		if repairPlanningEnabled(s) {
			next, err = repairDesignExtensionForSnapshot(s, t.ID, failedEvidence, s.RepairAttempts+1)
		} else {
			next, err = engineeringplan.RepairExtension(s.Graph.Graph, t.ID, failedEvidence, s.RepairAttempts+1)
		}
		if err != nil {
			return err
		}
		if _, err := appendGraphRevision(path, next); err != nil {
			return err
		}
		return nil
	}
	return errors.New("no failed gate eligible for repair evolution")
}

// runGraphResearchBatch schedules a frozen research batch with bounded
// parallelism and records exact explorer evidence for each task.
func runGraphResearchBatch(ctx context.Context, controllerPath string, s Snapshot, tasks []engineeringplan.Task) error {
	if len(tasks) == 0 {
		return errors.New("empty research batch")
	}
	specs, err := buildExplorerBatch(ctx, controllerPath, s, tasks)
	if err != nil {
		return err
	}
	maxParallel := s.Creation.Execution.EffectiveMaxParallel()
	remaining := s.Creation.Config.MaxExplorationRecords() - len(s.Explorations)
	if remaining < len(specs) {
		// Bound the batch to durable capacity; remaining tasks run next tick.
		if remaining <= 0 {
			return errors.New("exploration record bound reached")
		}
		specs = specs[:remaining]
	}
	workers := maxParallel
	if workers > len(specs) {
		workers = len(specs)
	}
	if workers < 1 {
		workers = 1
	}
	if workers > 8 {
		workers = 8
	}
	// Each dependency wave owns a distinct immutable schedule. The same
	// frozen cohort retains its identity across restart.
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	cohortID, err := graphCohortID(s.Graph.Digest, s.Graph.Revision, specs)
	if err != nil {
		return err
	}
	// Keep Windows filenames bounded; Bind still checks the full cohort
	// identity, so a filename-prefix collision rejects rather than dispatches.
	schedulerPath := controllerPath + ".graph-schedule-" + cohortID[:16] + ".jsonl"
	def := taskscheduler.Definition{Version: 1, Nonce: "graph-" + cohortID, Tasks: specs}
	if _, err := taskscheduler.Bind(schedulerPath, def); err != nil {
		return err
	}
	adapter := ScheduledDispatchAdapter{JournalPath: schedulerPath}
	pumpCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	pumpErr := make(chan error, 1)
	go func() {
		pumpErr <- taskscheduler.Pump(pumpCtx, schedulerPath, adapter, taskscheduler.PumpOptions{Workers: workers, PollInterval: 10 * time.Millisecond})
	}()
	// Finite termination: stop when all batch tasks reach terminal evidence,
	// not forever. Use existing schedule observations and cancellation, then
	// join owned calls.
	deadline := time.Now().Add(5 * time.Minute)
	for {
		snap, err := taskscheduler.Inspect(schedulerPath)
		if err != nil {
			cancel()
			return errors.Join(err, waitGraphPump(pumpErr, 5*time.Second))
		}
		done, failed := true, ""
		for _, spec := range specs {
			st, ok := snap.Tasks[spec.ID]
			if !ok {
				done = false
				break
			}
			switch st.Status {
			case taskscheduler.StatusSucceeded:
				// Complete only exact matching recorded ExplorerRecord and
				// scheduler terminal receipt.
				ctrl, err := Inspect(controllerPath)
				if err != nil {
					done = false
					break
				}
				found := false
				for _, rec := range ctrl.Explorations {
					if rec.Invocation.ID == spec.InvocationID {
						found = true
						break
					}
				}
				if !found || st.Evidence == nil || st.Evidence.InvocationID != spec.InvocationID {
					done = false
					break
				}
			case taskscheduler.StatusFailed, taskscheduler.StatusCancelled:
				failed = spec.ID
			default:
				done = false
			}
			if failed != "" {
				break
			}
		}
		if failed != "" {
			cancel()
			return errors.Join(fmt.Errorf("research batch task %q failed", failed), waitGraphPump(pumpErr, 5*time.Second))
		}
		if done {
			cancel()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			return errors.Join(errors.New("research batch timed out"), waitGraphPump(pumpErr, 5*time.Second))
		}
		if pumpCtx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			cancel()
			return errors.Join(ctx.Err(), waitGraphPump(pumpErr, 5*time.Second))
		case <-time.After(20 * time.Millisecond):
		}
		// Check pump errors without blocking.
		select {
		case err := <-pumpErr:
			if err != nil {
				return err
			}
			return errors.New("research pump exited before terminal batch evidence")
		default:
		}
	}
	// Join owned pump calls.
	if err := waitGraphPump(pumpErr, 5*time.Second); err != nil {
		return err
	}
	// Record durable graph progress for each completed research task.
	for _, spec := range specs {
		ctrl, err := Inspect(controllerPath)
		if err != nil {
			return err
		}
		// Find graph task by scheduler ID (same as graph task ID).
		task, ok := ctrl.Graph.Graph.Task(spec.ID)
		if !ok {
			return fmt.Errorf("research task %q missing from graph", spec.ID)
		}
		if _, done := ctrl.Graph.Evidence[spec.ID]; done {
			continue
		}
		// Verify exact matching record exists.
		found := false
		for _, rec := range ctrl.Explorations {
			if rec.Invocation.ID == spec.InvocationID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("research task %q lacks exact explorer record", spec.ID)
		}
		attemptID := fmt.Sprintf("attempt-%d", len(task.Attempts)+1)
		p := GraphProgress{Version: 1, PlanID: ctrl.Graph.PlanID, Digest: ctrl.Graph.Digest, TaskID: spec.ID, AttemptID: attemptID, Outcome: "completed", ExplorerInvocationID: spec.InvocationID}
		if _, err := recordGraphProgress(controllerPath, p); err != nil {
			return err
		}
	}
	return nil
}

func waitGraphPump(result <-chan error, grace time.Duration) error {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-result:
		return graphPumpError(err)
	case <-timer.C:
		// Existing provider intents retain ownership. No success or retry
		// authority is inferred from an adapter that has not joined.
		return errors.New("research calls remain pending after cancellation; observation required")
	}
}

func graphPumpError(err error) error {
	if err == nil || err == context.Canceled {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		remaining := []error{}
		for _, child := range joined.Unwrap() {
			if failure := graphPumpError(child); failure != nil {
				remaining = append(remaining, failure)
			}
		}
		return errors.Join(remaining...)
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok && graphPumpError(wrapped.Unwrap()) == nil {
		return nil
	}
	return err
}
