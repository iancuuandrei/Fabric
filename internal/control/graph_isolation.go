package control

import (
	"context"
	"errors"
	"path/filepath"
	"sort"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// GraphIsolationIntent binds one ready implementation task to a distinct,
// pristine source worktree. Creation is an effect: once recorded, a missing
// receipt remains UNKNOWN and is never recreated by this API.
type GraphIsolationIntent struct {
	Version         int              `json:"version"`
	PlanID          string           `json:"plan_id"`
	GraphDigest     string           `json:"graph_digest"`
	Revision        int              `json:"revision"`
	TaskID          string           `json:"task_id"`
	BaseCandidateID string           `json:"base_candidate_id"`
	ScopePaths      []string         `json:"scope_paths"`
	WritePaths      []string         `json:"write_paths"`
	Request         worktree.Request `json:"request"`
}

// GraphIsolationReceipt records the exact confirmed task worktree and its
// pristine candidate observation.
type GraphIsolationReceipt struct {
	Intent    GraphIsolationIntent `json:"intent"`
	Binding   worktree.Binding     `json:"binding"`
	Candidate worktree.Candidate   `json:"candidate"`
}

// GraphIsolationState is the replayed durable state for one isolated task.
type GraphIsolationState struct {
	Intent    GraphIsolationIntent `json:"intent"`
	Binding   *worktree.Binding    `json:"binding,omitempty"`
	Candidate *worktree.Candidate  `json:"candidate,omitempty"`
	Outcome   string               `json:"outcome"`
}

// GraphIsolationPreparation freezes the exact ready cohort and its declared
// scheduling estimates before any child workspace effect is admitted.
// Estimated is the overall work total (sum of all demands), not measured
// usage. Version 1 is the single-cohort isolated mode: SelectedTaskIDs must
// fit together. Version 2 is the opt-in resource-bounded wave mode:
// SelectedTaskIDs holds all initial ready implementations (up to eight) and
// Waves partitions them into nonempty resource-bounded waves derived
// deterministically via SelectResourceWaves; WaveEstimated holds each wave's
// per-wave peak and WaveBlocked retains each wave's bounded typed block
// provenance. Waves, WaveEstimated, and WaveBlocked are omitted for version 1
// to preserve absent-field legacy serialization; version 2 requires Waves and
// WaveEstimated with WaveBlocked length matching Waves. Version 3 is the
// staged dependent mode: it freezes the exact CURRENT ready implementation
// subset (hub then leaves) with CohortIndex keying the stage order,
// BaseCandidateID binding the exact parent candidate for that stage, and
// Waves partitioning the current subset via the same resource-bounded
// derivation. CohortIndex is omitted for versions 1 and 2 to preserve
// absent-field legacy serialization. CohortSelectorVersion records the
// frozen per-wave selector for version 3: absent (zero) preserves the
// greedy derivation byte-for-byte, 1 records the exact finite
// lexicographic optimum over the same hard gates. It is omitted for
// versions 1 and 2 and for greedy version 3 runs.
type GraphIsolationPreparation struct {
	Version         int                                     `json:"version"`
	PlanID          string                                  `json:"plan_id"`
	GraphDigest     string                                  `json:"graph_digest"`
	Revision        int                                     `json:"revision"`
	BaseCandidateID string                                  `json:"base_candidate_id"`
	Capacity        engineeringplan.ResourceCapacity        `json:"capacity"`
	Demands         []engineeringplan.TaskResourceDemand    `json:"demands"`
	SelectedTaskIDs []string                                `json:"selected_task_ids"`
	Estimated       engineeringplan.ResourceTotals          `json:"estimated"`
	Waves           [][]string                              `json:"waves,omitempty"`
	WaveEstimated   []engineeringplan.ResourceTotals        `json:"wave_estimated,omitempty"`
	WaveBlocked     [][]engineeringplan.ResourceBlockReason `json:"wave_blocked,omitempty"`
	CohortIndex     int                                     `json:"cohort_index,omitempty"`
	// CohortSelectorVersion is the frozen staged wave selector: 0 (absent)
	// is the greedy derivation, 1 is the exact lexicographic optimum.
	CohortSelectorVersion int    `json:"cohort_selector_version,omitempty"`
	PreparationID         string `json:"preparation_id"`
}

// IsolationEstimateTemplate is immutable policy input. Exact task IDs and the
// configured writer route are derived after graph acceptance.
type IsolationEstimateTemplate struct {
	CPUMilli          int64 `json:"cpu_milli"`
	MemoryMiB         int64 `json:"memory_mib"`
	VerificationSlots int   `json:"verification_slots"`
	RuntimeSlots      int   `json:"runtime_slots"`
}

// Validate bounds declared per-writer estimates; writers may consume zero
// verification slots because native verification follows parent integration.
func (t IsolationEstimateTemplate) Validate() error {
	if t.CPUMilli < 1 || t.MemoryMiB < 1 || t.VerificationSlots < 0 || t.RuntimeSlots < 1 {
		return errors.New("invalid isolation estimate template")
	}
	return nil
}

func isolatedImplementationEnabled(s Snapshot) bool {
	return s.Creation.Execution != nil && (s.Creation.Execution.IsolatedImplementationVersion == 1 || s.Creation.Execution.IsolatedImplementationVersion == 2 || s.Creation.Execution.IsolatedImplementationVersion == 3)
}

func isolatedWavesEnabled(s Snapshot) bool {
	return s.Creation.Execution != nil && s.Creation.Execution.IsolatedImplementationVersion == 2
}

func stagedIsolationEnabled(s Snapshot) bool {
	return s.Creation.Execution != nil && s.Creation.Execution.IsolatedImplementationVersion == 3
}

// InspectTaskIsolation returns the durable state only; it does not observe,
// create, or retry a workspace.
func InspectTaskIsolation(path, taskID string) (GraphIsolationState, error) {
	s, err := Inspect(path)
	if err != nil {
		return GraphIsolationState{}, err
	}
	state, ok := s.GraphIsolations[taskID]
	if !ok {
		return GraphIsolationState{}, errors.New("task isolation not admitted")
	}
	return state, nil
}

// PrepareGraphIsolationCohort freezes the selected resource-bounded ready
// cohort before a child workspace is created.
func PrepareGraphIsolationCohort(ctx context.Context, path string) (GraphIsolationPreparation, error) {
	s, err := Inspect(path)
	if err != nil {
		return GraphIsolationPreparation{}, err
	}
	if !isolatedImplementationEnabled(s) || s.GraphIsolationPreparation != nil {
		return GraphIsolationPreparation{}, errors.New("graph isolation cohort already prepared or disabled")
	}
	if len(s.GraphIsolations) != 0 {
		return GraphIsolationPreparation{}, errors.New("graph isolation already admitted")
	}
	prep, err := admitGraphIsolationPreparation(ctx, s)
	if err != nil {
		return GraphIsolationPreparation{}, err
	}
	if err := Append(path, "graph.isolation-prepared", prep); err != nil {
		return GraphIsolationPreparation{}, err
	}
	return prep, nil
}

// CreateTaskIsolation appends intent before creating one task-owned worktree.
// It is intentionally initial-source only; future overlay isolation requires a
// separate explicit capability rather than silently reusing changed candidates.
func CreateTaskIsolation(ctx context.Context, path, taskID string) (state GraphIsolationState, err error) {
	s, err := Inspect(path)
	if err != nil {
		return GraphIsolationState{}, err
	}
	if err := RequireDispatchAllowed(s); err != nil {
		return GraphIsolationState{}, err
	}
	if !isolatedImplementationEnabled(s) {
		return GraphIsolationState{}, errors.New("isolated implementation not enabled")
	}
	if err = requireCurrentHostAdmission(ctx, s); err != nil {
		return GraphIsolationState{}, err
	}
	if s.Workspace == nil {
		return GraphIsolationState{}, errors.New("parent workspace required")
	}
	if s.GraphIsolationPreparation == nil {
		return GraphIsolationState{}, errors.New("frozen graph isolation cohort required")
	}
	if existing, ok := s.GraphIsolations[taskID]; ok {
		if existing.Outcome == "UNKNOWN" {
			return existing, errors.New("task isolation outcome UNKNOWN; explicit reconciliation required")
		}
		return existing, nil
	}
	parentLease, err := worktree.AcquireRead(s.Workspace.Request)
	if err != nil {
		return GraphIsolationState{}, err
	}
	defer func() { err = errors.Join(err, parentLease.Close()) }()
	intent, task, err := admitGraphIsolationIntent(ctx, s, taskID)
	if err != nil {
		return GraphIsolationState{}, err
	}
	if err = Append(path, "graph.isolate-intent", intent); err != nil {
		return GraphIsolationState{}, err
	}
	stateRoot, err := controllerNamespace(s.Creation)
	if err != nil || stateRoot == "" {
		return GraphIsolationState{Intent: intent, Outcome: "UNKNOWN"}, errors.New("isolated implementation requires external controller state root")
	}
	if err = safepath.EnsureDirectory(stateRoot, "graph-isolates/"+intent.Request.RunID); err != nil {
		return GraphIsolationState{Intent: intent, Outcome: "UNKNOWN"}, err
	}
	lease, err := worktree.Acquire(intent.Request)
	if err != nil {
		return GraphIsolationState{Intent: intent, Outcome: "UNKNOWN"}, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	binding, err := worktree.Create(ctx, intent.Request)
	if err != nil {
		return GraphIsolationState{Intent: intent, Outcome: "UNKNOWN"}, err
	}
	candidate, err := worktree.Pristine(ctx, binding)
	if err != nil {
		return GraphIsolationState{Intent: intent, Outcome: "UNKNOWN"}, err
	}
	if err = Append(path, "graph.isolate-confirmed", GraphIsolationReceipt{Intent: intent, Binding: binding, Candidate: candidate}); err != nil {
		return GraphIsolationState{Intent: intent, Outcome: "UNKNOWN"}, err
	}
	_ = task
	return InspectTaskIsolation(path, taskID)
}

func expectedGraphIsolationIntent(s Snapshot, taskID string) (GraphIsolationIntent, engineeringplan.Task, error) {
	if s.State != "IMPLEMENTING" || s.Graph == nil || s.Workspace == nil || s.Candidate == nil || s.FileIntent != nil {
		return GraphIsolationIntent{}, engineeringplan.Task{}, errors.New("pristine initial graph candidate required")
	}
	ready, err := graphReadyTasks(s)
	if err != nil {
		return GraphIsolationIntent{}, engineeringplan.Task{}, err
	}
	var task engineeringplan.Task
	found := false
	for _, candidate := range ready {
		if candidate.ID == taskID {
			task, found = candidate, true
			break
		}
	}
	if !found || task.Kind != engineeringplan.Implementation {
		return GraphIsolationIntent{}, engineeringplan.Task{}, errors.New("ready implementation task required")
	}
	if s.GraphIsolationPreparation == nil || !containsGraphIsolationTask(s.GraphIsolationPreparation.SelectedTaskIDs, taskID) {
		return GraphIsolationIntent{}, engineeringplan.Task{}, errors.New("task is not in frozen isolation cohort")
	}
	baseID, err := s.Candidate.ID()
	if err != nil {
		return GraphIsolationIntent{}, engineeringplan.Task{}, err
	}
	runID, err := canonical.Hash("harness.graph-isolation.v1", struct {
		ParentRunID, Digest, TaskID, BaseCandidateID, PreparationID string
		Revision                                                    int
	}{s.RunID, s.Graph.Digest, task.ID, baseID, s.GraphIsolationPreparation.PreparationID, s.Graph.Revision})
	if err != nil {
		return GraphIsolationIntent{}, engineeringplan.Task{}, err
	}
	req, err := worktree.Prepare(runID, s.Creation.Repository)
	if err != nil {
		return GraphIsolationIntent{}, engineeringplan.Task{}, err
	}
	req.CandidateIdentity = s.Creation.Config.CandidateIdentity
	stateRoot, err := controllerNamespace(s.Creation)
	if err != nil || stateRoot == "" {
		return GraphIsolationIntent{}, engineeringplan.Task{}, errors.New("isolated implementation requires external controller state root")
	}
	req.ControllerStateRoot = filepath.Join(stateRoot, "graph-isolates", runID)
	if err := req.Validate(); err != nil {
		return GraphIsolationIntent{}, engineeringplan.Task{}, err
	}
	return GraphIsolationIntent{Version: 1, PlanID: s.Graph.PlanID, GraphDigest: s.Graph.Digest, Revision: s.Graph.Revision, TaskID: task.ID, BaseCandidateID: baseID, ScopePaths: append([]string(nil), task.ScopePaths...), WritePaths: append([]string(nil), task.WritePaths...), Request: req}, task, nil
}

func admitGraphIsolationIntent(ctx context.Context, s Snapshot, taskID string) (GraphIsolationIntent, engineeringplan.Task, error) {
	if s.Workspace == nil || s.Candidate == nil {
		return GraphIsolationIntent{}, engineeringplan.Task{}, errors.New("parent workspace required")
	}
	base, err := worktree.Pristine(ctx, *s.Workspace)
	if err != nil || base != *s.Candidate {
		return GraphIsolationIntent{}, engineeringplan.Task{}, errors.New("base candidate changed")
	}
	return expectedGraphIsolationIntent(s, taskID)
}

func containsGraphIsolationTask(ids []string, taskID string) bool {
	for _, id := range ids {
		if id == taskID {
			return true
		}
	}
	return false
}

func expectedGraphIsolationPreparation(s Snapshot) (GraphIsolationPreparation, error) {
	if s.State != "IMPLEMENTING" || s.Graph == nil || s.Workspace == nil || s.Candidate == nil || s.FileIntent != nil {
		return GraphIsolationPreparation{}, errors.New("pristine initial graph candidate required")
	}
	baseID, err := s.Candidate.ID()
	if err != nil {
		return GraphIsolationPreparation{}, err
	}
	ready, err := graphReadyTasks(s)
	if err != nil {
		return GraphIsolationPreparation{}, err
	}
	var implementations []engineeringplan.Task
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			implementations = append(implementations, task)
		}
	}
	if s.Creation.Execution.IsolationCapacity == nil || s.Creation.Execution.IsolationEstimate == nil {
		return GraphIsolationPreparation{}, errors.New("isolation capacity and template required")
	}
	demands, err := isolationResourceDemands(s, implementations)
	if err != nil {
		return GraphIsolationPreparation{}, err
	}
	if isolatedWavesEnabled(s) {
		return expectedGraphIsolationWavesPreparation(s, baseID, implementations, demands)
	}
	cohort, err := engineeringplan.SelectResourceCohort(s.Graph.Graph, implementations, demands, *s.Creation.Execution.IsolationCapacity, s.Creation.Execution.EffectiveMaxParallel())
	if err != nil {
		return GraphIsolationPreparation{}, err
	}
	if len(cohort.Tasks) == 0 || len(cohort.Tasks) != len(implementations) {
		return GraphIsolationPreparation{}, errors.New("resource cohort selected no implementation tasks")
	}
	ids := make([]string, len(cohort.Tasks))
	for i, task := range cohort.Tasks {
		ids[i] = task.ID
	}
	prep := GraphIsolationPreparation{Version: 1, PlanID: s.Graph.PlanID, GraphDigest: s.Graph.Digest, Revision: s.Graph.Revision, BaseCandidateID: baseID, Capacity: *s.Creation.Execution.IsolationCapacity, Demands: demands, SelectedTaskIDs: ids, Estimated: cohort.Estimated}
	prep.PreparationID, err = canonical.Hash("harness.graph-isolation-preparation.v1", prep)
	return prep, err
}

func expectedGraphIsolationWavesPreparation(s Snapshot, baseID string, implementations []engineeringplan.Task, demands []engineeringplan.TaskResourceDemand) (GraphIsolationPreparation, error) {
	if len(implementations) == 0 || len(implementations) > 8 {
		return GraphIsolationPreparation{}, errors.New("resource waves require one to eight ready implementations")
	}
	waves, err := engineeringplan.SelectResourceWaves(s.Graph.Graph, implementations, demands, *s.Creation.Execution.IsolationCapacity, s.Creation.Execution.EffectiveMaxParallel())
	if err != nil {
		return GraphIsolationPreparation{}, err
	}
	if len(waves) == 0 {
		return GraphIsolationPreparation{}, errors.New("resource waves selected no implementation tasks")
	}
	ids := make([]string, 0, len(implementations))
	for _, task := range implementations {
		ids = append(ids, task.ID)
	}
	sortStrings(ids)
	waveIDs, waveEstimated, waveBlocked, seen, err := collectWavePartition(waves, "resource waves contain an empty wave", "resource waves duplicated a task")
	if err != nil {
		return GraphIsolationPreparation{}, err
	}
	if len(seen) != len(implementations) {
		return GraphIsolationPreparation{}, errors.New("resource waves do not cover all ready implementations")
	}
	total := engineeringplan.SumResourceDemands(demands)
	prep := GraphIsolationPreparation{Version: 2, PlanID: s.Graph.PlanID, GraphDigest: s.Graph.Digest, Revision: s.Graph.Revision, BaseCandidateID: baseID, Capacity: *s.Creation.Execution.IsolationCapacity, Demands: demands, SelectedTaskIDs: ids, Estimated: total, Waves: waveIDs, WaveEstimated: waveEstimated, WaveBlocked: waveBlocked}
	prep.PreparationID, err = canonical.Hash("harness.graph-isolation-preparation.v2", prep)
	return prep, err
}

func admitGraphIsolationPreparation(ctx context.Context, s Snapshot) (GraphIsolationPreparation, error) {
	if s.Workspace == nil || s.Candidate == nil {
		return GraphIsolationPreparation{}, errors.New("parent workspace required")
	}
	lease, err := worktree.AcquireRead(s.Workspace.Request)
	if err != nil {
		return GraphIsolationPreparation{}, err
	}
	defer lease.Close()
	base, err := worktree.Pristine(ctx, *s.Workspace)
	if err != nil || base != *s.Candidate {
		return GraphIsolationPreparation{}, errors.New("base candidate changed")
	}
	return expectedGraphIsolationPreparation(s)
}

func sortStrings(values []string) {
	sort.Strings(values)
}

func replayGraphIsolation(s *Snapshot, e journal.Event) error {
	if !isolatedImplementationEnabled(*s) {
		return errors.New("isolated implementation not enabled")
	}
	if s.GraphIsolations == nil {
		s.GraphIsolations = map[string]GraphIsolationState{}
	}
	switch e.Kind {
	case "graph.isolation-prepared":
		var prep GraphIsolationPreparation
		if err := canonical.Decode(e.Payload, &prep); err != nil {
			return err
		}
		if s.GraphIsolationPreparation != nil || len(s.GraphIsolations) != 0 {
			return errors.New("graph isolation preparation already recorded")
		}
		if prep.Version != 1 && prep.Version != 2 {
			return errors.New("invalid graph isolation preparation version")
		}
		if s.Creation.Execution == nil {
			return errors.New("isolated implementation not enabled")
		}
		if s.Creation.Execution.IsolatedImplementationVersion == 1 && prep.Version != 1 {
			return errors.New("graph isolation preparation version differs from isolated policy")
		}
		if s.Creation.Execution.IsolatedImplementationVersion == 2 && prep.Version != 2 {
			return errors.New("graph isolation preparation version differs from isolated wave policy")
		}
		if prep.Version == 1 && (len(prep.Waves) != 0 || len(prep.WaveEstimated) != 0 || len(prep.WaveBlocked) != 0) {
			return errors.New("legacy graph isolation preparation carries wave fields")
		}
		if prep.Version == 2 && (len(prep.Waves) == 0 || len(prep.WaveEstimated) != len(prep.Waves) || len(prep.WaveBlocked) != len(prep.Waves)) {
			return errors.New("graph isolation waves are incomplete")
		}
		expected, err := expectedGraphIsolationPreparation(*s)
		if err != nil || !sameCanonical(expected, prep) {
			return errors.New("graph isolation preparation substitution")
		}
		s.GraphIsolationPreparation = &prep
	case "graph.isolate-intent":
		var intent GraphIsolationIntent
		if err := canonical.Decode(e.Payload, &intent); err != nil {
			return err
		}
		if len(s.GraphIsolations) >= 8 {
			return errors.New("graph isolation bound exceeded")
		}
		if _, exists := s.GraphIsolations[intent.TaskID]; exists {
			return errors.New("graph isolation already admitted")
		}
		expected, _, err := expectedGraphIsolationIntent(*s, intent.TaskID)
		if err != nil || !sameCanonical(expected, intent) {
			return errors.New("graph isolation intent substitution")
		}
		s.GraphIsolations[intent.TaskID] = GraphIsolationState{Intent: intent, Outcome: "UNKNOWN"}
	case "graph.isolate-confirmed":
		var receipt GraphIsolationReceipt
		if err := canonical.Decode(e.Payload, &receipt); err != nil {
			return err
		}
		state, ok := s.GraphIsolations[receipt.Intent.TaskID]
		if !ok || state.Outcome != "UNKNOWN" || !sameCanonical(state.Intent, receipt.Intent) || receipt.Binding.Request != state.Intent.Request {
			return errors.New("graph isolation receipt substitution")
		}
		if _, err := receipt.Binding.ID(); err != nil {
			return err
		}
		if err := receipt.Candidate.ValidateBinding(receipt.Binding); err != nil {
			return err
		}
		baseID, err := isolationReceiptBaseID(*s, receipt.Intent.BaseCandidateID, "graph isolation base candidate missing", "graph isolation base candidate substitution")
		if err != nil {
			return err
		}
		_ = baseID
		base := *s.Candidate
		child := receipt.Candidate
		if child.Version != base.Version || child.Head != base.Head || child.IndexHash != base.IndexHash || child.FilesHash != base.FilesHash || child.FileCount != base.FileCount {
			return errors.New("graph isolation child is not pristine frozen source")
		}
		state.Binding, state.Candidate, state.Outcome = &receipt.Binding, &receipt.Candidate, "CONFIRMED"
		s.GraphIsolations[receipt.Intent.TaskID] = state
	default:
		return errors.New("unknown graph isolation event")
	}
	return nil
}
