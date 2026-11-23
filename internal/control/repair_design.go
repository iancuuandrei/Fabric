package control

import (
	"errors"
	"fmt"
	"sort"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
)

func repairPlanningEnabled(s Snapshot) bool {
	return s.Creation.Execution != nil && s.Creation.Execution.RepairPlanningVersion == 1
}

func repairDesignExtensionForSnapshot(s Snapshot, failedTaskID, failedEvidence string, seq int) (engineeringplan.Graph, error) {
	scope, err := initialImplementationScope(s)
	if err != nil {
		return engineeringplan.Graph{}, err
	}
	if s.Graph == nil {
		return engineeringplan.Graph{}, errors.New("repair design extension requires a recorded graph")
	}
	return engineeringplan.RepairDesignExtensionScoped(s.Graph.Graph, failedTaskID, failedEvidence, seq, scope)
}

// initialImplementationScope derives the immutable write ceiling from the
// accepted plan, never from a repair task or reviewer/explorer text.
func initialImplementationScope(s Snapshot) ([]string, error) {
	g, err := parseAcceptedGraph(s)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var scope []string
	for _, task := range g.Tasks {
		if task.Kind == engineeringplan.Implementation {
			for _, p := range task.ScopePaths {
				if !seen[p] {
					seen[p] = true
					scope = append(scope, p)
				}
			}
		}
	}
	if len(scope) == 0 {
		return nil, errors.New("accepted graph has no initial implementation scope")
	}
	sort.Strings(scope)
	return scope, nil
}

func repairFailureID(ev GraphTaskEvidence) string {
	if ev.ReviewInvocationID != "" {
		return ev.ReviewInvocationID
	}
	if ev.VerificationPlanID != "" {
		return ev.VerificationPlanID
	}
	return ev.AttemptID
}

func validateRepairDesignExtension(s Snapshot, next engineeringplan.Graph) error {
	if s.Graph == nil || s.Creation.Execution == nil || s.RepairAttempts >= s.Creation.Execution.MaxRepairs {
		return errors.New("repair design extension outside repair budget")
	}
	seq := s.RepairAttempts + 1
	var design *engineeringplan.Task
	for i := range next.Tasks {
		candidate := &next.Tasks[i]
		if _, existed := s.Graph.Graph.Task(candidate.ID); existed || candidate.Kind != engineeringplan.Design || candidate.ParentID == "" {
			continue
		}
		for _, expected := range candidate.ExpectedEvidence {
			if expected.Kind == "repair-attempt" && expected.Description == fmt.Sprintf("%d", seq) {
				design = candidate
				break
			}
		}
	}
	if design == nil {
		return errors.New("repair revision lacks its bounded design task")
	}
	evidence, ok := s.Graph.Evidence[design.ParentID]
	if !ok || evidence.Outcome != "failed" || repairFailureID(evidence) == "" {
		return errors.New("repair design does not bind a completed failed gate")
	}
	legacyExpected, err := engineeringplan.RepairDesignExtension(s.Graph.Graph, design.ParentID, repairFailureID(evidence), seq)
	if err != nil {
		return err
	}
	if sameCanonicalGraph(legacyExpected, next) {
		return nil
	}
	scope, err := initialImplementationScope(s)
	if err != nil {
		return err
	}
	scopedExpected, err := engineeringplan.RepairDesignExtensionScoped(s.Graph.Graph, design.ParentID, repairFailureID(evidence), seq, scope)
	if err != nil {
		return err
	}
	if !sameCanonicalGraph(scopedExpected, next) {
		return errors.New("repair design extension differs from deterministic failed-gate extension")
	}
	return nil
}

func validateRepairWriteRefinement(s Snapshot, next engineeringplan.Graph) error {
	if s.Graph == nil || s.Candidate == nil {
		return errors.New("repair write refinement requires graph and current candidate")
	}
	var changed *engineeringplan.Task
	for i := range s.Graph.Graph.Tasks {
		old := s.Graph.Graph.Tasks[i]
		fresh, ok := next.Task(old.ID)
		if !ok {
			return errors.New("repair write refinement removed an existing task")
		}
		if sameGraphTask(old, fresh) {
			continue
		}
		if changed != nil || old.Kind != engineeringplan.Implementation || old.Completed || len(old.Attempts) != 0 || len(old.WritePaths) != 0 || fresh.ID != old.ID || fresh.ParentID != old.ParentID || fresh.Kind != old.Kind || fresh.Title != old.Title || !stringListsEqual(fresh.Dependencies, old.Dependencies) || !stringListsEqual(fresh.ScopePaths, old.ScopePaths) || !engineeringplanEvidenceEqual(fresh.ExpectedEvidence, old.ExpectedEvidence) || fresh.EstimatedSeconds != old.EstimatedSeconds || fresh.Completed || len(fresh.Attempts) != 0 || len(fresh.WritePaths) == 0 {
			return errors.New("repair revision changed more than one unstarted repair write list")
		}
		changed = &fresh
	}
	if changed == nil || len(next.Tasks) != len(s.Graph.Graph.Tasks) {
		return errors.New("repair revision is not a write-path refinement")
	}
	seq := ""
	for _, expected := range changed.ExpectedEvidence {
		if expected.Kind == "repair-attempt" {
			seq = expected.Description
		}
	}
	if seq == "" || changed.ParentID == "" {
		return errors.New("repair implementation lacks gate and slot identity")
	}
	failure, ok := s.Graph.Evidence[changed.ParentID]
	if !ok || failure.Outcome != "failed" {
		return errors.New("repair implementation does not bind a failed gate")
	}
	var design *engineeringplan.Task
	for _, dependency := range changed.Dependencies {
		task, ok := s.Graph.Graph.Task(dependency)
		if ok && task.Kind == engineeringplan.Design && task.ParentID == changed.ParentID {
			for _, expected := range task.ExpectedEvidence {
				if expected.Kind == "repair-attempt" && expected.Description == seq {
					design = &task
				}
			}
		}
	}
	if design == nil || !design.Completed {
		return errors.New("repair implementation lacks its completed matching design task")
	}
	designEvidence, ok := s.Graph.Evidence[design.ID]
	if !ok || designEvidence.Outcome != "completed" || designEvidence.ExplorerInvocationID == "" {
		return errors.New("repair design lacks completed explorer evidence")
	}
	var record *ExplorerRecord
	for i := range s.Explorations {
		if s.Explorations[i].Invocation.ID == designEvidence.ExplorerInvocationID {
			record = &s.Explorations[i]
			break
		}
	}
	if record == nil {
		return errors.New("repair design explorer record missing")
	}
	question, err := explorerQuestionForTask(s, *design)
	if err != nil || record.Question != question {
		return errors.New("repair design invocation does not bind exact task context")
	}
	var proposal Exploration
	if err := canonical.Decode([]byte(record.Result.Output), &proposal); err != nil {
		return err
	}
	candidateID, err := s.Candidate.ID()
	if err != nil || proposal.CandidateID != candidateID {
		return errors.New("repair design result does not bind the current candidate")
	}
	if !stringListsEqual(proposal.Paths, changed.WritePaths) {
		return errors.New("repair write paths differ from exact recorded design result")
	}
	scope, err := initialImplementationScope(s)
	if err != nil {
		return err
	}
	expected, err := engineeringplan.RefineRepairWritePaths(s.Graph.Graph, changed.ID, proposal.Paths, scope)
	if err != nil {
		return err
	}
	if !sameCanonicalGraph(expected, next) {
		return errors.New("repair write refinement differs from candidate-bound design result")
	}
	return nil
}

func refineRepairWritePathsFromDesign(path string, s Snapshot, task engineeringplan.Task) error {
	if !repairPlanningEnabled(s) || s.Graph == nil || len(task.WritePaths) != 0 {
		return errors.New("repair design refinement is not enabled for this task")
	}
	seq := ""
	for _, expected := range task.ExpectedEvidence {
		if expected.Kind == "repair-attempt" {
			seq = expected.Description
		}
	}
	if seq == "" {
		return errors.New("empty-write implementation is not a repair design slot")
	}
	var design *engineeringplan.Task
	for _, dependency := range task.Dependencies {
		candidate, ok := s.Graph.Graph.Task(dependency)
		if ok && candidate.Kind == engineeringplan.Design && candidate.ParentID == task.ParentID {
			for _, expected := range candidate.ExpectedEvidence {
				if expected.Kind == "repair-attempt" && expected.Description == seq && candidate.Completed {
					design = &candidate
				}
			}
		}
	}
	if design == nil {
		return errors.New("repair implementation cannot start before its exact design task completes")
	}
	evidence, ok := s.Graph.Evidence[design.ID]
	if !ok || evidence.Outcome != "completed" || evidence.ExplorerInvocationID == "" {
		return errors.New("repair design has no recorded explorer evidence")
	}
	var record *ExplorerRecord
	for i := range s.Explorations {
		if s.Explorations[i].Invocation.ID == evidence.ExplorerInvocationID {
			record = &s.Explorations[i]
			break
		}
	}
	if record == nil {
		return errors.New("repair design record is missing")
	}
	var proposal Exploration
	if err := canonical.Decode([]byte(record.Result.Output), &proposal); err != nil {
		return err
	}
	if len(proposal.Paths) == 0 {
		return errors.New("repair design found no concrete in-scope paths")
	}
	scope, err := initialImplementationScope(s)
	if err != nil {
		return err
	}
	next, err := engineeringplan.RefineRepairWritePaths(s.Graph.Graph, task.ID, proposal.Paths, scope)
	if err != nil {
		return err
	}
	if err := validateRepairWriteRefinement(s, next); err != nil {
		return err
	}
	_, err = appendGraphRevision(path, next)
	return err
}

func sameGraphTask(a, b engineeringplan.Task) bool {
	return a.ID == b.ID && a.ParentID == b.ParentID && a.Kind == b.Kind && a.Title == b.Title &&
		stringListsEqual(a.Dependencies, b.Dependencies) && stringListsEqual(a.ScopePaths, b.ScopePaths) &&
		stringListsEqual(a.WritePaths, b.WritePaths) && engineeringplanEvidenceEqual(a.ExpectedEvidence, b.ExpectedEvidence) &&
		a.EstimatedSeconds == b.EstimatedSeconds && a.Completed == b.Completed && equalTaskAttempts(a.Attempts, b.Attempts)
}

func equalTaskAttempts(a, b []engineeringplan.Attempt) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringListsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func engineeringplanEvidenceEqual(a, b []engineeringplan.Evidence) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
