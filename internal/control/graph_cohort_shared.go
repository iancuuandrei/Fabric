package control

import (
	"context"
	"errors"
	"reflect"
	"sort"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// Shared bounded helpers for staged (v3/v4), isolated initial (v1) and
// isolated wave (v2) cohorts. Each helper preserves exact version-specific
// error identities supplied by callers; helpers never merge differing
// authority gates. Serialized fields, W2 source/candidate authority and
// UNKNOWN handling stay at the call sites.

// isolationResourceDemands builds per-task demands from the frozen isolation
// template and writer route. Callers retain version-specific admission.
func isolationResourceDemands(s Snapshot, implementations []engineeringplan.Task) ([]engineeringplan.TaskResourceDemand, error) {
	if s.Creation.Execution.IsolationCapacity == nil || s.Creation.Execution.IsolationEstimate == nil {
		return nil, errors.New("isolation capacity and template required")
	}
	template := *s.Creation.Execution.IsolationEstimate
	profileID, err := canonical.Hash("harness.isolation-writer-profile.v1", *s.Creation.Config.Writer)
	if err != nil {
		return nil, err
	}
	route := engineeringplan.RuntimeResourceKey{ProfileID: profileID, Provider: s.Creation.Config.Writer.Provider, Model: s.Creation.Config.Writer.Model}
	demands := make([]engineeringplan.TaskResourceDemand, len(implementations))
	for i, task := range implementations {
		demands[i] = engineeringplan.TaskResourceDemand{TaskID: task.ID, CPUMilli: template.CPUMilli, MemoryMiB: template.MemoryMiB, VerificationSlots: template.VerificationSlots, Runtime: route, RuntimeSlots: template.RuntimeSlots}
	}
	return demands, nil
}

// collectWavePartition converts selected resource waves into frozen ID,
// estimate and blocked slices with duplicate/empty detection.
func collectWavePartition(waves []engineeringplan.ResourceCohort, emptyErr, duplicateErr string) ([][]string, []engineeringplan.ResourceTotals, [][]engineeringplan.ResourceBlockReason, map[string]bool, error) {
	waveIDs := make([][]string, 0, len(waves))
	waveEstimated := make([]engineeringplan.ResourceTotals, 0, len(waves))
	waveBlocked := make([][]engineeringplan.ResourceBlockReason, 0, len(waves))
	seen := map[string]bool{}
	for _, wave := range waves {
		if len(wave.Tasks) == 0 {
			return nil, nil, nil, nil, errors.New(emptyErr)
		}
		waveIDList := make([]string, 0, len(wave.Tasks))
		for _, task := range wave.Tasks {
			if seen[task.ID] {
				return nil, nil, nil, nil, errors.New(duplicateErr)
			}
			seen[task.ID] = true
			waveIDList = append(waveIDList, task.ID)
		}
		waveIDs = append(waveIDs, waveIDList)
		waveEstimated = append(waveEstimated, wave.Estimated)
		waveBlocked = append(waveBlocked, append([]engineeringplan.ResourceBlockReason{}, wave.Blocked...))
	}
	return waveIDs, waveEstimated, waveBlocked, seen, nil
}

// requireCohortTasksDisjoint rejects cohorts whose members are not pairwise
// independent and disjoint.
func requireCohortTasksDisjoint(tasks []engineeringplan.Task, errMsg string) error {
	for i := range tasks {
		for j := i + 1; j < len(tasks); j++ {
			if !tasksIndependentAndDisjoint(tasks[i], tasks[j]) {
				return errors.New(errMsg)
			}
		}
	}
	return nil
}

// requireCohortTasksInSelection rejects cohorts with members outside the
// frozen selection.
func requireCohortTasksInSelection(tasks []engineeringplan.Task, selected []string, errMsg string) error {
	for _, task := range tasks {
		if !containsGraphIsolationTask(selected, task.ID) {
			return errors.New(errMsg)
		}
	}
	return nil
}

// sortedFrozenCohortIDs checks that a requested set exactly matches the frozen
// cohort, returning the sorted request.
func sortedFrozenCohortIDs(requested, selected []string, sizeErr, memberErr string) ([]string, error) {
	ids := append([]string(nil), requested...)
	sort.Strings(ids)
	frozen := append([]string(nil), selected...)
	sort.Strings(frozen)
	if len(ids) != len(frozen) {
		return nil, errors.New(sizeErr)
	}
	for i := range ids {
		if ids[i] == "" || (i > 0 && ids[i] == ids[i-1]) || ids[i] != frozen[i] {
			return nil, errors.New(memberErr)
		}
	}
	return ids, nil
}

// readyImplementationByID indexes currently ready implementation tasks.
func readyImplementationByID(s Snapshot) (map[string]engineeringplan.Task, error) {
	ready, err := graphReadyTasks(s)
	if err != nil {
		return nil, err
	}
	readyByID := map[string]engineeringplan.Task{}
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			readyByID[task.ID] = task
		}
	}
	return readyByID, nil
}

// checkWaveMembership verifies wave tasks are exact ready tasks within one
// frozen wave and cover it exactly.
func checkWaveMembership(waveTasks []engineeringplan.Task, frozen []string, readyByID map[string]engineeringplan.Task, staleErr, outsideErr, coverErr string) error {
	got := make([]string, 0, len(waveTasks))
	for _, task := range waveTasks {
		readyTask, ok := readyByID[task.ID]
		if !ok || !reflect.DeepEqual(task, readyTask) {
			return errors.New(staleErr)
		}
		if !containsGraphIsolationTask(frozen, task.ID) {
			return errors.New(outsideErr)
		}
		got = append(got, task.ID)
	}
	sort.Strings(got)
	for i := range got {
		if got[i] != frozen[i] {
			return errors.New(coverErr)
		}
	}
	return nil
}

// staticGraphWriterRuntimeAdmitted reports whether a task-bound writer
// invocation runtime may enter the static graph-writer cohort. Codex is the
// shared-workspace route; opencode-http is admitted only for the opt-in
// isolated/staged cohorts, where every task invocation is child-bound and
// taskID-scoped, so no shared-workspace opencode task can route through the
// scheduler on the isolation flag alone.
func staticGraphWriterRuntimeAdmitted(s Snapshot, writerRuntime string) bool {
	if writerRuntime == "codex-app-server" {
		return true
	}
	return writerRuntime == "opencode-http" && isolatedImplementationEnabled(s)
}

// buildWriterTaskSpecs derives deterministic invocation-bound scheduler specs.
func buildWriterTaskSpecs(s Snapshot, controllerPath string, tasks []engineeringplan.Task) ([]taskscheduler.TaskSpec, error) {
	specs := make([]taskscheduler.TaskSpec, 0, len(tasks))
	for _, task := range tasks {
		if task.Kind != engineeringplan.Implementation {
			return nil, errors.New("writer cohort contains a non-implementation")
		}
		invocation, err := writerInvocationForTask(s, task.ID)
		if err != nil {
			return nil, err
		}
		if !staticGraphWriterRuntimeAdmitted(s, invocation.Profile.Runtime) {
			return nil, errors.New("parallel graph writer runtime unsupported")
		}
		specs = append(specs, taskscheduler.TaskSpec{ID: task.ID, RunID: s.RunID, ControllerPath: controllerPath, Operation: taskscheduler.OperationWriter, InvocationID: invocation.ID})
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	return specs, nil
}

// isolationReceiptBaseID validates the receipt base against the current
// candidate and frozen preparation without touching child authority.
func isolationReceiptBaseID(s Snapshot, intentBaseID, missingErr, substitutionErr string) (string, error) {
	if s.Candidate == nil || s.GraphIsolationPreparation == nil {
		return "", errors.New(missingErr)
	}
	baseID, err := s.Candidate.ID()
	if err != nil || intentBaseID != baseID || s.GraphIsolationPreparation.BaseCandidateID != baseID {
		return "", errors.New(substitutionErr)
	}
	return baseID, nil
}

// checkBatchMembers validates aggregate member evidence and returns member
// task IDs in record order. Evidence comparison stays version-specific at the
// call site via the supplied record check.
func checkBatchMembers(s Snapshot, members []GraphWriterMember, emptyErr, memberErr, childErr string, checkRecord func(member GraphWriterMember) (string, error)) ([]string, error) {
	ids := make([]string, 0, len(members))
	seen := map[string]bool{}
	if len(members) == 0 || len(members) > 8 {
		return nil, errors.New(emptyErr)
	}
	for _, member := range members {
		if seen[member.TaskID] || member.IsolationID == "" || member.ChildCandidateID == "" {
			return nil, errors.New(memberErr)
		}
		seen[member.TaskID] = true
		childID, err := checkRecord(member)
		if err != nil {
			return nil, err
		}
		if childID != member.ChildCandidateID {
			return nil, errors.New(childErr)
		}
		ids = append(ids, member.TaskID)
	}
	return ids, nil
}

// resolveWriterBatchMemberIDs validates the aggregate parent candidate and
// member evidence, returning member task IDs. Callers retain exact
// version-specific gates (aggregate version, isolation enablement, frozen
// preparation identity); only the identical parent/member body is shared via
// supplied error identities.
func resolveWriterBatchMemberIDs(s Snapshot, batch GraphWriterBatchRecord, parentErr, emptyErr, memberErr, childErr, evidenceErr string) ([]string, error) {
	candidateID, err := s.Candidate.ID()
	if err != nil || batch.CandidateID != candidateID || s.GraphIsolationPreparation.BaseCandidateID != candidateID {
		return nil, errors.Join(errors.New(parentErr), err)
	}
	return checkBatchMembers(s, batch.Members, emptyErr, memberErr, childErr, func(member GraphWriterMember) (string, error) {
		record, ok := s.GraphWriterResults[member.TaskID]
		if !ok || record.Isolated == nil || graphWriterRecordInvocation(record).ID != member.InvocationID || record.Isolated.IsolationID != member.IsolationID {
			return "", errors.New(evidenceErr)
		}
		childID, idErr := record.Isolated.Candidate.ID()
		if idErr != nil {
			return "", errors.Join(errors.New(childErr), idErr)
		}
		return childID, nil
	})
}

// buildFrozenWaveSpecs validates frozen wave membership and derives
// deterministic invocation-bound specs. Callers retain exact
// version-specific gates (isolation enablement, preparation version, wave
// index bounds); only the identical frozen-body is shared via supplied error
// identities.
func buildFrozenWaveSpecs(s Snapshot, controllerPath string, waveTasks []engineeringplan.Task, waveIndex int, membershipErr, staleErr, outsideErr, disjointErr string) ([]taskscheduler.TaskSpec, error) {
	frozen := append([]string(nil), s.GraphIsolationPreparation.Waves[waveIndex]...)
	sort.Strings(frozen)
	if len(waveTasks) == 0 || len(waveTasks) != len(frozen) {
		return nil, errors.New(membershipErr)
	}
	readyByID, err := readyImplementationByID(s)
	if err != nil {
		return nil, err
	}
	if err := checkWaveMembership(waveTasks, frozen, readyByID, staleErr, outsideErr, membershipErr); err != nil {
		return nil, err
	}
	if err := requireCohortTasksDisjoint(waveTasks, disjointErr); err != nil {
		return nil, err
	}
	return buildWriterTaskSpecs(s, controllerPath, waveTasks)
}

// validateAggregateProposalEffect checks a supplied parent proposal against
// freshly derived child changes and returns the expected prepared effect.
func validateAggregateProposalEffect(s Snapshot, supplied PreparedFiles, changes []fileeffects.Change, nonceFull, identityErr, afterErr, preparedErr string) (PreparedFiles, error) {
	proposal := supplied.Proposal
	if proposal.Version != 1 || proposal.Nonce != nonceFull || proposal.Before != *s.Candidate || !sameCanonical(proposal.Changes, changes) {
		return PreparedFiles{}, errors.New(identityErr)
	}
	if err := proposal.Validate(); err != nil {
		return PreparedFiles{}, err
	}
	after, err := composeGraphWriterCandidate(proposal.Before, proposal.BeforeFiles, changes)
	if err != nil || after != proposal.After {
		return PreparedFiles{}, errors.Join(errors.New(afterErr), err)
	}
	expected, err := preparedFiles(s, proposal)
	if err != nil {
		return PreparedFiles{}, err
	}
	if !sameCanonical(expected, supplied) {
		return PreparedFiles{}, errors.New(preparedErr)
	}
	return expected, nil
}

// proposeAggregateParentEffect freezes the current parent, prepares the
// aggregate proposal and returns the latest snapshot with its prepared effect.
func proposeAggregateParentEffect(ctx context.Context, path string, s Snapshot, isolationPreparationID, nonceFull, changedErr, preimageErr string, changes []fileeffects.Change) (Snapshot, PreparedFiles, string, error) {
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return Snapshot{}, PreparedFiles{}, "", err
	}
	lease, err := worktree.AcquireRead(s.Workspace.Request)
	if err != nil {
		return Snapshot{}, PreparedFiles{}, "", err
	}
	defer func() { _ = lease.Close() }()
	latest, err := Inspect(path)
	if err != nil {
		return Snapshot{}, PreparedFiles{}, "", err
	}
	if latest.Candidate == nil || *latest.Candidate != *s.Candidate || latest.GraphIsolationPreparation == nil || latest.GraphIsolationPreparation.PreparationID != isolationPreparationID {
		return Snapshot{}, PreparedFiles{}, "", errors.New(changedErr)
	}
	proposal, err := fileeffects.Prepare(ctx, *latest.Workspace, nonceFull, changes)
	if err != nil {
		return Snapshot{}, PreparedFiles{}, "", err
	}
	if err := fileeffects.Preflight(ctx, *latest.Workspace, proposal); err != nil {
		return Snapshot{}, PreparedFiles{}, "", err
	}
	if proposal.Before != *latest.Candidate {
		return Snapshot{}, PreparedFiles{}, "", errors.New(preimageErr)
	}
	prepared, err := preparedFiles(latest, proposal)
	if err != nil {
		return Snapshot{}, PreparedFiles{}, "", err
	}
	return latest, prepared, candidateID, nil
}

// freezeCohortBindings creates one isolation per frozen task, admits its
// context question and returns confirmed bindings. Creation and binding
// functions preserve version-specific (staged vs isolated) authority.
func freezeCohortBindings(ctx context.Context, path string, ids []string, create func(context.Context, string, string) error, binding func(Snapshot, string) (isolatedWriterBinding, error)) ([]isolatedWriterBinding, error) {
	s, err := Inspect(path)
	if err != nil {
		return nil, err
	}
	for _, taskID := range ids {
		if err := create(ctx, path, taskID); err != nil {
			return nil, err
		}
		s, err = Inspect(path)
		if err != nil {
			return nil, err
		}
		question, err := graphWriterTaskQuestion(s, taskID)
		if err != nil {
			return nil, err
		}
		if err := maybeAdmitIsolatedWriterTaskContext(ctx, path, taskID, question); err != nil {
			return nil, err
		}
	}
	s, err = Inspect(path)
	if err != nil {
		return nil, err
	}
	bindings := make([]isolatedWriterBinding, 0, len(ids))
	for _, taskID := range ids {
		b, err := binding(s, taskID)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, b)
	}
	return bindings, nil
}

// waveTasksForCohort resolves frozen wave IDs against the admitted cohort.
func waveTasksForCohort(byID map[string]engineeringplan.Task, waveIDs []string, errMsg string) ([]engineeringplan.Task, error) {
	waveTasks := make([]engineeringplan.Task, 0, len(waveIDs))
	for _, id := range waveIDs {
		task, ok := byID[id]
		if !ok {
			return nil, errors.New(errMsg)
		}
		waveTasks = append(waveTasks, task)
	}
	return waveTasks, nil
}

// waveProposalStatus reports whether every wave task already has a
// candidate-bound isolated proposal, rejecting partial waves.
func waveProposalStatus(latest Snapshot, waveTasks []engineeringplan.Task, partialErr string) (bool, error) {
	allProposed := true
	for _, task := range waveTasks {
		record, ok := latest.GraphWriterResults[task.ID]
		if !ok || record.Isolated == nil {
			allProposed = false
			break
		}
	}
	if allProposed {
		return true, nil
	}
	for _, task := range waveTasks {
		if _, ok := latest.GraphWriterResults[task.ID]; ok {
			return false, errors.New(partialErr)
		}
	}
	return false, nil
}

// scheduleWaveCohort binds and runs one wave schedule, then verifies a
// complete candidate-bound proposal set settled.
func scheduleWaveCohort(ctx context.Context, controllerPath string, latest Snapshot, specs []taskscheduler.TaskSpec, preparationID string, waveIndex int, schedulePrefix, hashLabel, capacityErr, settledErr string) error {
	workers := latest.Creation.Execution.EffectiveMaxParallel()
	if workers > len(specs) {
		workers = len(specs)
	}
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		return errors.New(capacityErr)
	}
	id, err := graphCohortID(latest.Graph.Digest, latest.Graph.Revision, specs)
	if err != nil {
		return err
	}
	waveNonce, err := canonical.Hash(hashLabel, struct {
		PreparationID string
		WaveIndex     int
		CohortID      string
	}{preparationID, waveIndex, id})
	if err != nil {
		return err
	}
	schedulePath := controllerPath + schedulePrefix + waveNonce[:16] + ".jsonl"
	definition := taskscheduler.Definition{Version: 1, Nonce: schedulePrefix[1:] + waveNonce, Tasks: specs}
	if _, err := taskscheduler.Bind(schedulePath, definition); err != nil {
		return err
	}
	if err := runScheduledGraphWriterCohort(ctx, controllerPath, schedulePath, specs, workers, true); err != nil {
		return err
	}
	after, err := Inspect(controllerPath)
	if err != nil {
		return err
	}
	for _, spec := range specs {
		record, ok := after.GraphWriterResults[spec.ID]
		if !ok || record.Isolated == nil {
			return errors.New(settledErr)
		}
	}
	return nil
}

// inspectUnrecordedWriterMember reloads state for one aggregate member,
// reporting already-recorded members so callers skip them. A zero task with
// nil error means the member task disappeared; callers emit their
// version-specific diagnostic.
func inspectUnrecordedWriterMember(path string, member GraphWriterMember, candidateID string) (Snapshot, engineeringplan.Task, bool, error) {
	latest, err := Inspect(path)
	if err != nil {
		return Snapshot{}, engineeringplan.Task{}, false, err
	}
	if evidence, ok := latest.Graph.Evidence[member.TaskID]; ok && evidence.Outcome == "completed" && evidence.CandidateID == candidateID && evidence.WriterInvocationID == member.InvocationID {
		return latest, engineeringplan.Task{}, true, nil
	}
	task, ok := latest.Graph.Graph.Task(member.TaskID)
	if !ok || task.Kind != engineeringplan.Implementation {
		return latest, engineeringplan.Task{}, false, nil
	}
	return latest, task, false, nil
}

// applyWriterBatchEffect applies a confirmed aggregate prepared effect.
func applyWriterBatchEffect(ctx context.Context, path string, s Snapshot) (Snapshot, error) {
	if err := autonomousDispatchBlocked(s); err != nil {
		return s, err
	}
	authorization, err := autonomousFileAuthorization(s, s.GraphWriterBatch.Prepared.Intent)
	if err != nil {
		return s, err
	}
	if _, err := ApplyFiles(ctx, path, s.GraphWriterBatch.Prepared, authorization); err != nil {
		return InspectOr(s, path, err)
	}
	return Inspect(path)
}
