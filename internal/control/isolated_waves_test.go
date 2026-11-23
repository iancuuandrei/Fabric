package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
)

func isolatedWavesThreeWriterCreation(t *testing.T) Creation {
	t.Helper()
	c := parallelWriterCreation(t, 2)
	c.Execution.ParallelImplementationVersion = 0
	c.Execution.IsolatedImplementationVersion = 2
	c.Execution.MaxParallel = 2
	c.Config.PlannerContract = plannerContractGraphV7
	return applyTwoSlotIsolationCapacity(t, c)
}

func TestIsolatedWavesThreeWritersAcrossTwoWavesIntegrateOnce(t *testing.T) {
	c := isolatedWavesThreeWriterCreation(t)
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, isolatedThreeWriterGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Config.Codex.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	prep, err := PrepareGraphIsolationCohort(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if prep.Version != 2 || len(prep.Waves) != 2 {
		t.Fatalf("wave preparation did not split three writers into two waves: version=%d waves=%v", prep.Version, prep.Waves)
	}
	for _, wave := range prep.Waves {
		if len(wave) == 0 || len(wave) > 2 {
			t.Fatalf("wave is not resource bounded to capacity two: %v", prep.Waves)
		}
	}
	if len(prep.SelectedTaskIDs) != 3 || len(prep.WaveEstimated) != 2 {
		t.Fatalf("wave preparation did not retain all identities and per-wave estimates: %+v", prep)
	}
	ready, err := RunAutonomous(context.Background(), path)
	if err != nil {
		for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("wave run failure cause: %v", cause)
		}
		if latest, inspectErr := Inspect(path); inspectErr == nil {
			outcomes := map[string]string{}
			for id, isolation := range latest.GraphIsolations {
				outcomes[id] = isolation.Outcome
			}
			t.Logf("wave run state=%s results=%d prepared=%v isolation-outcomes=%v fileIntent=%v batch=%v", latest.State, len(latest.GraphWriterResults), latest.GraphIsolationPreparation != nil, outcomes, latest.FileIntent != nil, latest.GraphWriterBatch != nil)
		}
		if schedules, globErr := filepath.Glob(path + ".isolated-graph-writers-wave-*.jsonl"); globErr == nil {
			for _, schedulePath := range schedules {
				if schedule, inspectErr := taskscheduler.Inspect(schedulePath); inspectErr == nil {
					statuses := map[string]string{}
					for id, state := range schedule.Tasks {
						statuses[id] = string(state.Status)
					}
					t.Logf("wave schedule %s statuses: %v", schedulePath, statuses)
				}
			}
		}
		if events, journalErr := journal.Read(path); journalErr == nil {
			kinds := map[string]int{}
			for _, event := range events {
				kinds[event.Kind]++
			}
			t.Logf("wave journal kinds: %v", kinds)
		}
		t.Fatal(err)
	}
	if ready.State != "READY" || ready.GraphWriterBatch == nil || ready.GraphWriterBatch.Version != 2 || len(ready.GraphWriterBatch.Members) != 3 {
		t.Fatalf("wave run did not reach READY with a three-member aggregate: state=%s batch=%+v", ready.State, ready.GraphWriterBatch)
	}
	if ready.GraphWriterBatch.IsolationPreparationID != prep.PreparationID || !graphWriterBatchEffectApplied(ready) || ready.Verification == nil || ready.Review == nil {
		t.Fatalf("wave aggregate did not pass single parent effect and native gates")
	}
	if countJournalKind(t, path, "files.intent") != 1 || countJournalKind(t, path, "files.observed") != 1 {
		t.Fatal("wave cohort did not produce exactly one parent file effect")
	}
	if len(ready.GraphWriterResults) != 3 {
		t.Fatalf("wave run did not retain each proposal: %d", len(ready.GraphWriterResults))
	}
	schedules, err := filepath.Glob(path + ".isolated-graph-writers-wave-*.jsonl")
	if err != nil || len(schedules) != 2 {
		t.Fatalf("wave execution did not use two deterministic wave schedules: %v %v", schedules, err)
	}
	for _, schedulePath := range schedules {
		schedule, err := taskscheduler.Inspect(schedulePath)
		if err != nil {
			t.Fatal(err)
		}
		if len(schedule.Tasks) == 0 || len(schedule.Tasks) > 2 {
			t.Fatalf("wave schedule is not resource bounded: %s tasks=%d", schedulePath, len(schedule.Tasks))
		}
		for _, state := range schedule.Tasks {
			if state.Status != taskscheduler.StatusSucceeded {
				t.Fatalf("wave schedule did not succeed: %s %+v", schedulePath, state.Status)
			}
		}
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	maxEffective := 0
	for _, event := range events {
		if event.Kind != "graph.writer.memory-admitted" {
			continue
		}
		var record GraphMemoryAdmissionRecord
		if err := canonical.Decode(event.Payload, &record); err != nil {
			t.Fatal(err)
		}
		if record.Decision.EffectiveWorkers > 2 {
			t.Fatalf("memory admission exceeded per-wave capacity two: %+v", record.Decision)
		}
		if len(record.ActiveTaskIDs) > 2 {
			t.Fatalf("memory admission active set exceeded two: %+v", record.ActiveTaskIDs)
		}
		if record.Decision.EffectiveWorkers > maxEffective {
			maxEffective = record.Decision.EffectiveWorkers
		}
	}
	if maxEffective == 0 || maxEffective > 2 {
		t.Fatalf("per-wave admission ceiling was not retained: max=%d", maxEffective)
	}
	waveIndex := map[string]int{}
	for i, wave := range prep.Waves {
		for _, id := range wave {
			waveIndex[id] = i
		}
	}
	firstProposed := map[int]int{}
	firstAdmitted := map[int]int{}
	for seq, event := range events {
		if event.Kind == "graph.writer.proposed" {
			var record GraphWriterRecord
			if err := canonical.Decode(event.Payload, &record); err != nil {
				t.Fatal(err)
			}
			if idx, ok := waveIndex[record.TaskID]; ok {
				if _, exists := firstProposed[idx]; !exists {
					firstProposed[idx] = seq
				}
			}
		}
		if event.Kind == "graph.writer.memory-admitted" {
			var record GraphMemoryAdmissionRecord
			if err := canonical.Decode(event.Payload, &record); err != nil {
				t.Fatal(err)
			}
			if idx, ok := waveIndex[record.TaskID]; ok {
				if _, exists := firstAdmitted[idx]; !exists {
					firstAdmitted[idx] = seq
				}
			}
		}
	}
	if len(firstProposed) != 2 || len(firstAdmitted) != 2 {
		t.Fatalf("wave proposals/admissions are incomplete: proposed=%v admitted=%v", firstProposed, firstAdmitted)
	}
	if firstProposed[0] > firstAdmitted[1] {
		t.Fatalf("second wave was admitted before first wave proposals: proposed=%v admitted=%v", firstProposed, firstAdmitted)
	}
	for id, file := range map[string]string{"impl-alpha": "alpha.txt", "impl-beta": "beta.txt", "impl-gamma": "gamma.txt"} {
		if _, ok := ready.Graph.Evidence[id]; !ok {
			t.Fatalf("implementation %s lacks graph completion evidence", id)
		}
		content, err := os.ReadFile(filepath.Join(ready.Workspace.Request.Path, filepath.FromSlash(file)))
		if err != nil || string(content) != "parallel fixture output\n" {
			t.Fatalf("parent did not receive aggregate output %s: %q %v", file, content, err)
		}
	}
}

func TestIsolatedWavesRejectForeignStaleAndMissingProposal(t *testing.T) {
	c := isolatedWavesThreeWriterCreation(t)
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, isolatedThreeWriterGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	prep, err := PrepareGraphIsolationCohort(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if prep.Version != 2 || len(prep.Waves) != 2 {
		t.Fatalf("expected two waves, got %+v", prep.Waves)
	}
	current, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	foreign := *current.GraphIsolationPreparation
	foreign.Waves = append(append([][]string(nil), foreign.Waves...), []string{"impl-foreign"})
	if err := replayGraphIsolation(&current, journal.Event{Kind: "graph.isolation-prepared", Payload: mustCanonical(t, foreign)}); err == nil {
		t.Fatal("foreign wave preparation accepted")
	}
	stale := *current.GraphIsolationPreparation
	staleDemands := append([]engineeringplan.TaskResourceDemand(nil), stale.Demands...)
	staleDemands[0].CPUMilli++
	stale.Demands = staleDemands
	if err := replayGraphIsolation(&current, journal.Event{Kind: "graph.isolation-prepared", Payload: mustCanonical(t, stale)}); err == nil {
		t.Fatal("stale demand preparation accepted")
	}
	ready, err := graphReadyTasks(current)
	if err != nil {
		t.Fatal(err)
	}
	var implementations []engineeringplan.Task
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			implementations = append(implementations, task)
		}
	}
	if _, err := isolatedGraphWriterIDs(current, implementations[:2]); err == nil {
		t.Fatal("partial cohort accepted as complete initial set")
	}
	foreignTasks := append(append([]engineeringplan.Task(nil), implementations...), engineeringplan.Task{ID: "impl-foreign", Kind: engineeringplan.Implementation, Title: "foreign", ScopePaths: []string{"."}, WritePaths: []string{"foreign.txt"}})
	if _, err := buildIsolatedGraphWriterWaveSpecs(path, current, foreignTasks[:1], 0); err == nil {
		t.Fatal("foreign wave task accepted")
	}
}

func TestIsolatedWavesNoFitAndCeilingsAreSafeDiagnostics(t *testing.T) {
	graph := isolatedThreeWriterGraphFixture()
	ready, err := engineeringplan.Ready(graph)
	if err != nil {
		t.Fatal(err)
	}
	var implementations []engineeringplan.Task
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			implementations = append(implementations, task)
		}
	}
	route := engineeringplan.RuntimeResourceKey{ProfileID: "profile", Provider: "p", Model: "m"}
	demands := make([]engineeringplan.TaskResourceDemand, len(implementations))
	for i, task := range implementations {
		demands[i] = engineeringplan.TaskResourceDemand{TaskID: task.ID, CPUMilli: 1000, MemoryMiB: 512, VerificationSlots: 0, Runtime: route, RuntimeSlots: 1}
	}
	capacity := engineeringplan.ResourceCapacity{
		CPUMilli: 500, MemoryMiB: 8192, VerificationSlots: 2, TotalRuntimeSlots: 4,
		ProviderSlots: []engineeringplan.ProviderSlotLimit{{Provider: "p", Slots: 4}},
		ModelSlots:    []engineeringplan.ModelSlotLimit{{Model: engineeringplan.ProviderModelKey{Provider: "p", Model: "m"}, Slots: 4}},
		RuntimeSlots:  []engineeringplan.RuntimeSlotLimit{{Runtime: route, Slots: 4}},
	}
	if _, err := engineeringplan.SelectResourceWaves(graph, implementations, demands, capacity, 2); err == nil || !strings.Contains(err.Error(), "cpu_milli") {
		t.Fatalf("CPU no-fit did not yield a safe diagnostic: %v", err)
	}
	providerTight := engineeringplan.ResourceCapacity{
		CPUMilli: 4000, MemoryMiB: 8192, VerificationSlots: 3, TotalRuntimeSlots: 3,
		ProviderSlots: []engineeringplan.ProviderSlotLimit{{Provider: "p", Slots: 1}},
		ModelSlots:    []engineeringplan.ModelSlotLimit{{Model: engineeringplan.ProviderModelKey{Provider: "p", Model: "m"}, Slots: 3}},
		RuntimeSlots:  []engineeringplan.RuntimeSlotLimit{{Runtime: route, Slots: 3}},
	}
	waves, err := engineeringplan.SelectResourceWaves(graph, implementations, demandsForWaves(implementations, route), providerTight, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(waves) != 3 {
		t.Fatalf("provider ceiling did not force three serial waves: %d", len(waves))
	}
	runtimeTight := engineeringplan.ResourceCapacity{
		CPUMilli: 4000, MemoryMiB: 8192, VerificationSlots: 3, TotalRuntimeSlots: 1,
		ProviderSlots: []engineeringplan.ProviderSlotLimit{{Provider: "p", Slots: 3}},
		ModelSlots:    []engineeringplan.ModelSlotLimit{{Model: engineeringplan.ProviderModelKey{Provider: "p", Model: "m"}, Slots: 3}},
		RuntimeSlots:  []engineeringplan.RuntimeSlotLimit{{Runtime: route, Slots: 3}},
	}
	waves, err = engineeringplan.SelectResourceWaves(graph, implementations, demandsForWaves(implementations, route), runtimeTight, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(waves) != 3 {
		t.Fatalf("global runtime ceiling did not force three serial waves: %d", len(waves))
	}
}

func demandsForWaves(tasks []engineeringplan.Task, route engineeringplan.RuntimeResourceKey) []engineeringplan.TaskResourceDemand {
	demands := make([]engineeringplan.TaskResourceDemand, len(tasks))
	for i, task := range tasks {
		demands[i] = engineeringplan.TaskResourceDemand{TaskID: task.ID, CPUMilli: 1, MemoryMiB: 1, VerificationSlots: 0, Runtime: route, RuntimeSlots: 1}
	}
	return demands
}

func mustCanonical(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := canonical.Bytes(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestIsolatedWavesUnknownIsolationTamperRejected(t *testing.T) {
	c := isolatedWavesThreeWriterCreation(t)
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, isolatedThreeWriterGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareGraphIsolationCohort(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	current, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := graphReadyTasks(current)
	if err != nil {
		t.Fatal(err)
	}
	var implementations []engineeringplan.Task
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			implementations = append(implementations, task)
		}
	}
	forged := current
	forged.GraphIsolations = map[string]GraphIsolationState{
		implementations[0].ID: {Intent: GraphIsolationIntent{TaskID: implementations[0].ID}, Outcome: "UNKNOWN"},
	}
	if err := replayGraphIsolation(&forged, journal.Event{Kind: "graph.isolate-intent", Payload: mustCanonical(t, GraphIsolationIntent{TaskID: implementations[0].ID})}); err == nil {
		t.Fatal("UNKNOWN isolation tamper accepted")
	}
	if lifecycleUnresolved(current) != nil && len(lifecycleUnresolved(current)) != 0 {
		t.Logf("lifecycle unresolved before waves: %v", lifecycleUnresolved(current))
	}
}

func TestIsolatedWavesPlannerBindingReflectsVersion(t *testing.T) {
	c := isolatedWavesThreeWriterCreation(t)
	policy := c.Execution
	if policy.IsolatedImplementationVersion != 2 {
		t.Fatalf("wave fixture did not bind version two: %+v", policy)
	}
	invocation, err := plannerInvocationWithContextsAndRecipe(c.Config, "implement independent changes", nil, nil, policy)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Instruction string `json:"instruction"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.Instruction, "resource-bounded initial wave") {
		t.Fatalf("wave planner binding does not reflect version two: %s", payload.Instruction)
	}
}

func TestIsolatedV1CompatibilityPreserved(t *testing.T) {
	c := isolatedThreeWriterCreation(t)
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, isolatedThreeWriterGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	prep, err := PrepareGraphIsolationCohort(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if prep.Version != 1 || len(prep.Waves) != 0 || len(prep.WaveEstimated) != 0 || len(prep.WaveBlocked) != 0 {
		t.Fatalf("v1 preparation carries wave fields: %+v", prep)
	}
	raw, err := canonical.Bytes(prep)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"waves"`) || strings.Contains(string(raw), `"wave_estimated"`) || strings.Contains(string(raw), `"wave_blocked"`) {
		t.Fatalf("v1 serialization is not absent-field legacy: %s", raw)
	}
}

func TestIsolatedWavesRejectScopeReplan(t *testing.T) {
	c := isolatedWavesThreeWriterCreation(t)
	if c.Execution.ScopeReplanVersion != 0 {
		t.Fatalf("wave fixture must omit scope replanning, got version %d", c.Execution.ScopeReplanVersion)
	}
	rejected := *c.Execution
	rejected.ScopeReplanVersion = 2
	rejected.ScopeReplanDesignVersion = 2
	rejected.MaxScopeReplans = 2
	if err := rejected.Validate(); err == nil || !strings.Contains(err.Error(), "isolated waves do not support scope replanning") {
		t.Fatalf("waves+scope-replan admitted: %v", err)
	}
	if got := cohortScopeReplanMode(rejected); got == "parallel" || got == "isolated" {
		t.Fatalf("wave cohort mode mislabeled as %q", got)
	}
	v1 := isolatedThreeWriterCreation(t)
	v1.Execution.ScopeReplanVersion = 2
	v1.Execution.ScopeReplanDesignVersion = 2
	v1.Execution.MaxScopeReplans = 2
	if err := v1.Execution.Validate(); err != nil {
		t.Fatalf("v1 cohort scope-replan rejected: %v", err)
	}
	if got := cohortScopeReplanMode(*v1.Execution); got != "isolated" {
		t.Fatalf("v1 cohort mode changed: %q", got)
	}
	if cohortScopeReplanningEnabled(Snapshot{Creation: Creation{Execution: c.Execution}}) {
		t.Fatal("waves incorrectly report cohort scope replanning enabled")
	}
}

func TestIsolatedWavesPlannerBindingCaps(t *testing.T) {
	v1creation := isolatedThreeWriterCreation(t)
	v1inv, err := plannerInvocationWithContextsAndRecipe(v1creation.Config, "implement independent changes", nil, nil, v1creation.Execution)
	if err != nil {
		t.Fatal(err)
	}
	var v1payload struct {
		Instruction string                       `json:"instruction"`
		Isolation   *plannerIsolationConstraints `json:"isolation,omitempty"`
	}
	if err := json.Unmarshal([]byte(v1inv.Input), &v1payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v1payload.Instruction, "complete initial cohort must fit") {
		t.Fatalf("v1 instruction changed: %s", v1payload.Instruction)
	}
	if v1payload.Isolation == nil || v1payload.Isolation.IsolatedImplementationVersion != 0 {
		t.Fatalf("v1 constraints must omit version binding: %+v", v1payload.Isolation)
	}
	wavesCreation := isolatedWavesThreeWriterCreation(t)
	waveInv, err := plannerInvocationWithContextsAndRecipe(wavesCreation.Config, "implement independent changes", nil, nil, wavesCreation.Execution)
	if err != nil {
		t.Fatal(err)
	}
	var wavePayload struct {
		Instruction string                       `json:"instruction"`
		Isolation   *plannerIsolationConstraints `json:"isolation,omitempty"`
	}
	if err := json.Unmarshal([]byte(waveInv.Input), &wavePayload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wavePayload.Instruction, "at most 8 initial implementation tasks in total") || !strings.Contains(wavePayload.Instruction, "each wave at most 2") {
		t.Fatalf("v2 caps contradictory or missing: %s", wavePayload.Instruction)
	}
	if strings.Contains(wavePayload.Instruction, "Plan at most 2 initial implementation tasks,") {
		t.Fatalf("v2 retains contradictory max-parallel total cap: %s", wavePayload.Instruction)
	}
	if wavePayload.Isolation == nil || wavePayload.Isolation.IsolatedImplementationVersion != 2 {
		t.Fatalf("v2 constraints must bind version two: %+v", wavePayload.Isolation)
	}
}

func TestIsolatedWavesMemoryTailBound(t *testing.T) {
	c := isolatedWavesThreeWriterCreation(t)
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, isolatedThreeWriterGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Config.Codex.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	prep, err := PrepareGraphIsolationCohort(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(prep.Waves) != 2 {
		t.Fatalf("expected two waves, got %v", prep.Waves)
	}
	current, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	tailWave := current.GraphIsolationPreparation.Waves[len(current.GraphIsolationPreparation.Waves)-1]
	if len(tailWave) != 1 {
		t.Fatalf("expected tail wave of one, got %v", current.GraphIsolationPreparation.Waves)
	}
	tailWorkers := isolatedMemoryMaxWorkers(current, tailWave[0])
	if tailWorkers != 1 {
		t.Fatalf("tail admission bound is %d, want 1 for wave %v", tailWorkers, tailWave)
	}
	headWave := current.GraphIsolationPreparation.Waves[0]
	headWorkers := isolatedMemoryMaxWorkers(current, headWave[0])
	if headWorkers != 2 {
		t.Fatalf("head admission bound is %d, want 2 for wave %v", headWorkers, headWave)
	}
	if unknown := isolatedMemoryMaxWorkers(current, "impl-foreign"); unknown != 0 {
		t.Fatalf("foreign task admitted with %d workers", unknown)
	}
}

func TestIsolatedWavesTotalsSumAndPerWaveExact(t *testing.T) {
	c := isolatedWavesThreeWriterCreation(t)
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, isolatedThreeWriterGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	prep, err := PrepareGraphIsolationCohort(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(prep.WaveBlocked) != len(prep.Waves) {
		t.Fatalf("per-wave blocked provenance missing: waves=%d blocked=%d", len(prep.Waves), len(prep.WaveBlocked))
	}
	if got := engineeringplan.SumResourceDemands(prep.Demands); !sameCanonical(got, prep.Estimated) {
		t.Fatalf("overall estimated is not the exact demand sum")
	}
	for i, wave := range prep.Waves {
		if len(wave) == 0 {
			t.Fatalf("wave %d is empty", i)
		}
		if prep.WaveEstimated[i].TotalRuntimeSlots != len(wave) {
			t.Fatalf("wave %d estimated slots %d != members %d", i, prep.WaveEstimated[i].TotalRuntimeSlots, len(wave))
		}
	}
	raw, err := canonical.Bytes(prep)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"wave_blocked"`) {
		t.Fatalf("v2 preparation omits per-wave blocked provenance: %s", raw)
	}
}

type waveUnknownFaultAdapter struct {
	specs map[string]taskscheduler.TaskSpec
	fails map[string]int
	heads map[string]string
}

func (a *waveUnknownFaultAdapter) Probe(_ context.Context, request taskscheduler.ProbeRequest) (taskscheduler.Evidence, error) {
	head, ok := a.heads[request.Task.ID]
	if !ok {
		head = strings.Repeat("c", 64)
	}
	spec, ok := a.specs[request.Task.ID]
	if !ok {
		return taskscheduler.Evidence{}, errors.New("missing wave task")
	}
	return taskscheduler.Evidence{RunID: spec.RunID, InvocationID: spec.InvocationID, ControllerHead: head, Status: taskscheduler.StatusReady}, nil
}

func (a *waveUnknownFaultAdapter) Dispatch(_ context.Context, claim taskscheduler.Claim) (taskscheduler.Evidence, error) {
	a.fails[claim.Task.ID]++
	spec := a.specs[claim.Task.ID]
	return taskscheduler.Evidence{RunID: spec.RunID, InvocationID: spec.InvocationID, ControllerHead: strings.Repeat("e", 64), AdmissionID: strings.Repeat("a", 64), Status: taskscheduler.StatusUnknown}, nil
}

func (a *waveUnknownFaultAdapter) Reconcile(_ context.Context, claim taskscheduler.Claim) (taskscheduler.Evidence, error) {
	spec := a.specs[claim.Task.ID]
	return taskscheduler.Evidence{RunID: spec.RunID, InvocationID: spec.InvocationID, ControllerHead: strings.Repeat("e", 64), AdmissionID: strings.Repeat("a", 64), Status: taskscheduler.StatusUnknown}, nil
}

func TestIsolatedWavesFirstWaveUnknownStopsWithoutNextWaveDispatch(t *testing.T) {
	c := isolatedWavesThreeWriterCreation(t)
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, isolatedThreeWriterGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Config.Codex.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	prep, err := PrepareGraphIsolationCohort(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(prep.Waves) != 2 {
		t.Fatalf("expected two waves, got %v", prep.Waves)
	}
	if _, err := prepareIsolatedGraphWriterCohort(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	current, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := graphReadyTasks(current)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]engineeringplan.Task{}
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			byID[task.ID] = task
		}
	}
	var firstWaveTasks []engineeringplan.Task
	for _, id := range prep.Waves[0] {
		task, ok := byID[id]
		if !ok {
			t.Fatalf("first wave task %q not ready", id)
		}
		firstWaveTasks = append(firstWaveTasks, task)
	}
	specs, err := buildIsolatedGraphWriterWaveSpecs(path, current, firstWaveTasks, 0)
	if err != nil {
		t.Fatal(err)
	}
	schedulePath := filepath.Join(t.TempDir(), "wave-unknown.jsonl")
	definition := taskscheduler.Definition{Version: 1, Nonce: "wave-unknown-first", Tasks: specs}
	if _, err := taskscheduler.Bind(schedulePath, definition); err != nil {
		t.Fatal(err)
	}
	adapter := &waveUnknownFaultAdapter{specs: map[string]taskscheduler.TaskSpec{}, fails: map[string]int{}, heads: map[string]string{}}
	for _, spec := range specs {
		adapter.specs[spec.ID] = spec
		adapter.heads[spec.ID] = strings.Repeat("c", 64)
	}
	decision, err := taskscheduler.Tick(context.Background(), schedulePath, adapter)
	if err != nil || decision.Status != taskscheduler.StatusUnknown {
		t.Fatalf("first wave fault did not yield genuine UNKNOWN: decision=%+v err=%v", decision, err)
	}
	schedule, err := taskscheduler.Inspect(schedulePath)
	if err != nil {
		t.Fatal(err)
	}
	unknown := 0
	for _, state := range schedule.Tasks {
		if state.Status == taskscheduler.StatusUnknown {
			unknown++
		}
	}
	if unknown == 0 {
		t.Fatal("scheduler recorded no UNKNOWN task")
	}
	if _, err := taskscheduler.RecoverClaim(context.Background(), schedulePath, decision.ClaimID, adapter); err != nil {
		t.Fatal(err)
	}
	if adapter.fails[decision.TaskID] != 1 {
		t.Fatalf("UNKNOWN was redispatched: counts=%v", adapter.fails)
	}
	latest, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if latest.FileIntent != nil || len(latest.GraphWriterResults) != 0 {
		t.Fatal("first-wave UNKNOWN produced a file effect or proposal")
	}
	for taskID, state := range latest.GraphIsolations {
		if state.Outcome != "CONFIRMED" {
			t.Fatalf("invocation ownership not retained for %q: %q", taskID, state.Outcome)
		}
	}
	globbed, err := filepath.Glob(path + ".isolated-graph-writers-wave-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(globbed) != 0 {
		t.Fatalf("next wave was scheduled after UNKNOWN: %v", globbed)
	}
}

func TestIsolatedWavesSecondWaveUnknownBlocksResume(t *testing.T) {
	c := isolatedWavesThreeWriterCreation(t)
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, isolatedThreeWriterGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Config.Codex.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	prep, err := PrepareGraphIsolationCohort(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(prep.Waves) != 2 {
		t.Fatalf("expected two waves, got %v", prep.Waves)
	}
	if _, err := prepareIsolatedGraphWriterCohort(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	current, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := graphReadyTasks(current)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]engineeringplan.Task{}
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			byID[task.ID] = task
		}
	}
	var firstWave, secondWave []engineeringplan.Task
	for _, id := range prep.Waves[0] {
		firstWave = append(firstWave, byID[id])
	}
	for _, id := range prep.Waves[1] {
		secondWave = append(secondWave, byID[id])
	}
	firstSpecs, err := buildIsolatedGraphWriterWaveSpecs(path, current, firstWave, 0)
	if err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(t.TempDir(), "wave-first-terminal.jsonl")
	if _, err := taskscheduler.Bind(firstPath, taskscheduler.Definition{Version: 1, Nonce: "wave-first-terminal", Tasks: firstSpecs}); err != nil {
		t.Fatal(err)
	}
	succeed := &waveUnknownFaultAdapter{specs: map[string]taskscheduler.TaskSpec{}, fails: map[string]int{}, heads: map[string]string{}}
	for _, spec := range firstSpecs {
		succeed.specs[spec.ID] = spec
		succeed.heads[spec.ID] = strings.Repeat("c", 64)
	}
	firstDecision, err := taskscheduler.Tick(context.Background(), firstPath, &waveSucceedAdapter{inner: succeed})
	if err != nil {
		t.Fatal(err)
	}
	_ = firstDecision
	secondSpecs, err := buildIsolatedGraphWriterWaveSpecs(path, current, secondWave, 1)
	if err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(t.TempDir(), "wave-second-unknown.jsonl")
	if _, err := taskscheduler.Bind(secondPath, taskscheduler.Definition{Version: 1, Nonce: "wave-second-unknown", Tasks: secondSpecs}); err != nil {
		t.Fatal(err)
	}
	fault := &waveUnknownFaultAdapter{specs: map[string]taskscheduler.TaskSpec{}, fails: map[string]int{}, heads: map[string]string{}}
	for _, spec := range secondSpecs {
		fault.specs[spec.ID] = spec
		fault.heads[spec.ID] = strings.Repeat("c", 64)
	}
	unknownDecision, err := taskscheduler.Tick(context.Background(), secondPath, fault)
	if err != nil || unknownDecision.Status != taskscheduler.StatusUnknown {
		t.Fatalf("second wave fault did not yield genuine UNKNOWN: %+v %v", unknownDecision, err)
	}
	if _, err := taskscheduler.RecoverClaim(context.Background(), secondPath, unknownDecision.ClaimID, fault); err != nil {
		t.Fatal(err)
	}
	if fault.fails[unknownDecision.TaskID] != 1 {
		t.Fatalf("second-wave UNKNOWN redispatched: %v", fault.fails)
	}
	latest, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if latest.FileIntent != nil {
		t.Fatal("UNKNOWN second wave produced a file effect")
	}
}

type waveSucceedAdapter struct {
	inner *waveUnknownFaultAdapter
}

func (a *waveSucceedAdapter) Probe(ctx context.Context, request taskscheduler.ProbeRequest) (taskscheduler.Evidence, error) {
	return a.inner.Probe(ctx, request)
}

func (a *waveSucceedAdapter) Dispatch(_ context.Context, claim taskscheduler.Claim) (evidence taskscheduler.Evidence, err error) {
	spec := a.inner.specs[claim.Task.ID]
	a.inner.fails[claim.Task.ID]++
	return taskscheduler.Evidence{RunID: spec.RunID, InvocationID: spec.InvocationID, ControllerHead: strings.Repeat("e", 64), AdmissionID: strings.Repeat("a", 64), Status: taskscheduler.StatusSucceeded}, nil
}

func (a *waveSucceedAdapter) Reconcile(ctx context.Context, claim taskscheduler.Claim) (taskscheduler.Evidence, error) {
	return a.inner.Reconcile(ctx, claim)
}
