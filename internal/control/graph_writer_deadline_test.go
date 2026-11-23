package control

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/memoryadmission"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
)

func deadlineTestSnapshot(host *config.OpenCodeHost, writerRuntime string, isolatedVersion int) Snapshot {
	s := Snapshot{}
	s.Creation.Config.Writer = &runtime.Profile{Runtime: writerRuntime, Provider: "fixture-provider", Model: "fixture-model", Role: "writer"}
	s.Creation.Config.OpenCode = host
	if isolatedVersion != 0 {
		s.Creation.Execution = &ExecutionPolicy{IsolatedImplementationVersion: isolatedVersion}
	} else {
		s.Creation.Execution = &ExecutionPolicy{ParallelImplementationVersion: 1}
	}
	return s
}

func TestGraphWriterCohortTimeoutExposesFiveMinuteMismatch(t *testing.T) {
	host := &config.OpenCodeHost{Version: 1, InvocationTimeoutSeconds: 600}
	s := deadlineTestSnapshot(host, "opencode-http", 3)
	got := graphWriterCohortTimeout(s, 2, 2)
	// Leaves-wave shape from the observed trial: 2 writers, max-parallel 2.
	// One legal invocation alone needs 600s; the frozen 5-minute cohort
	// deadline cannot admit even one configured invocation, let alone
	// the finite admitted serial work.
	if legacyGraphWriterCohortTimeout != 5*time.Minute {
		t.Fatalf("legacy cohort timeout changed: got %s", legacyGraphWriterCohortTimeout)
	}
	if got <= legacyGraphWriterCohortTimeout {
		t.Fatalf("derived cohort timeout did not exceed legacy 5min: got %s", got)
	}
	// Serial worst case: 2 tasks x (1 initial + 2 admitted corrections) x
	// 600s = 6 finite legal invocations. No ceil(N/W) discount applies
	// because memory admission can serialize the pair (see the
	// EffectiveWorkers reduction test below) while scheduler slots remain.
	if want := 6 * 600 * time.Second; got != want {
		t.Fatalf("leaves cohort timeout mismatch: got %s want %s (2 tasks x 3 legal invocations x 600s, serial)", got, want)
	}
	if got < 600*time.Second {
		t.Fatalf("derived cohort timeout still narrower than one configured invocation: got %s", got)
	}
}

// TestGraphWriterMemoryAdmissionSerializesConfiguredPair proves the factual
// premise behind the serial cohort bound: with a configured maximum of two
// workers, one pressured observation reduces the future-claim ceiling to one
// effective worker. The configured maximum stays stable; only the effective
// ceiling drops, and BeforeClaim parks any claim beyond it. A ceil(N/W)
// timeout would therefore assume parallelism the gate does not guarantee.
func TestGraphWriterMemoryAdmissionSerializesConfiguredPair(t *testing.T) {
	pressured, err := memoryadmission.Next(nil,
		memoryadmission.Observation{Status: memoryadmission.ObservationObserved, Source: memoryadmission.SourceLinuxProcMeminfo, AvailableMiB: 3000, TotalMiB: 16000},
		2, 2048, memoryadmission.DefaultReserveMiB, memoryadmission.DefaultHysteresisMiB)
	if err != nil {
		t.Fatal(err)
	}
	if pressured.ConfiguredMaxWorkers != 2 {
		t.Fatalf("configured maximum changed under pressure: got %d want 2", pressured.ConfiguredMaxWorkers)
	}
	if pressured.Mode != memoryadmission.ModePressured || pressured.EffectiveWorkers != 1 {
		t.Fatalf("pressured pair did not serialize future claims: %+v", pressured)
	}
}

func TestGraphWriterCohortTimeoutDefaultsAndExplicitLimits(t *testing.T) {
	cases := []struct {
		name string
		host *config.OpenCodeHost
		want time.Duration
	}{
		{"default nil host", nil, openCodeProviderRuntimeTimeout},
		{"explicit 600s", &config.OpenCodeHost{Version: 1, InvocationTimeoutSeconds: 600}, 600 * time.Second},
		{"explicit 7200s", &config.OpenCodeHost{Version: 1, InvocationTimeoutSeconds: 7200}, 7200 * time.Second},
		{"zero falls back to default", &config.OpenCodeHost{Version: 1}, openCodeProviderRuntimeTimeout},
		{"negative falls back to default", &config.OpenCodeHost{Version: 1, InvocationTimeoutSeconds: -5}, openCodeProviderRuntimeTimeout},
		{"above bound falls back to default", &config.OpenCodeHost{Version: 1, InvocationTimeoutSeconds: 7201}, openCodeProviderRuntimeTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := deadlineTestSnapshot(tc.host, "opencode-http", 3)
			perInvocation, ok := effectiveGraphWriterPerInvocationTimeout(s)
			if !ok {
				t.Fatal("OpenCode cohort not recognized as deadline-derived")
			}
			if perInvocation != tc.want {
				t.Fatalf("per-invocation limit mismatch: got %s want %s", perInvocation, tc.want)
			}
			// Cross-check against the real provider runtime helper: both must
			// enforce the same frozen finite limit from the same host record.
			ctx, cancel := openCodeProviderRuntimeContext(context.Background(), tc.host)
			defer cancel()
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("provider runtime context has no deadline")
			}
			remaining := time.Until(deadline)
			if remaining <= 0 || remaining > perInvocation || perInvocation-remaining > 5*time.Second {
				t.Fatalf("provider runtime deadline diverges from cohort per-invocation limit: remaining=%s perInvocation=%s", remaining, perInvocation)
			}
			// Single task: exactly initial plus two admitted corrections,
			// serial by construction.
			if got := graphWriterCohortTimeout(s, 1, 1); got != 3*tc.want {
				t.Fatalf("single-task cohort timeout mismatch: got %s want %s", got, 3*tc.want)
			}
		})
	}
}

func TestGraphWriterCohortTimeoutSerialIgnoresWorkersAndBoundsCorrections(t *testing.T) {
	host := &config.OpenCodeHost{Version: 1, InvocationTimeoutSeconds: 600}
	s := deadlineTestSnapshot(host, "opencode-http", 1)
	if graphWriterMaxCorrectionsPerTask != 2 {
		t.Fatalf("correction allowance changed: got %d want 2", graphWriterMaxCorrectionsPerTask)
	}
	// The serial bound is identical for any advertised worker count: memory
	// admission and claim contention can park claims down to one effective
	// worker, so workers never discount the finite admitted work.
	for _, workers := range []int{1, 2, 4, 8} {
		if got, want := graphWriterCohortTimeout(s, 4, workers), 12*600*time.Second; got != want {
			t.Fatalf("4-task cohort with %d workers mismatch: got %s want %s (4 tasks x 3 invocations x 600s, serial)", workers, got, want)
		}
	}
	// Hub shape from the trial family: 3 tasks need nine finite serial
	// invocations however many scheduler slots are advertised.
	for _, workers := range []int{1, 2, 3} {
		if got, want := graphWriterCohortTimeout(s, 3, workers), 9*600*time.Second; got != want {
			t.Fatalf("hub cohort with %d workers mismatch: got %s want %s", workers, got, want)
		}
	}
	// Oversized worker counts grant no discount either.
	if got, want := graphWriterCohortTimeout(s, 2, 8), 6*600*time.Second; got != want {
		t.Fatalf("oversized worker count changed cohort bound: got %s want %s", got, want)
	}
	// Maximum legal shape stays finite and exact without overflow or an
	// artificial cap: 8 tasks x 3 invocations x 7200s.
	maxHost := &config.OpenCodeHost{Version: 1, InvocationTimeoutSeconds: 7200}
	maxSnapshot := deadlineTestSnapshot(maxHost, "opencode-http", 3)
	if got, want := graphWriterCohortTimeout(maxSnapshot, 8, 1), time.Duration(24)*7200*time.Second; got != want {
		t.Fatalf("maximum cohort bound mismatch: got %s want %s", got, want)
	} else if got <= 0 {
		t.Fatalf("maximum cohort bound overflowed: got %s", got)
	}
	if got := graphWriterCohortTimeout(maxSnapshot, 8, 8); got != time.Duration(24)*7200*time.Second {
		t.Fatalf("maximum cohort bound depends on workers: got %s", got)
	}
}

func TestGraphWriterCohortTimeoutInvalidTasksAndLegacy(t *testing.T) {
	host := &config.OpenCodeHost{Version: 1, InvocationTimeoutSeconds: 600}
	s := deadlineTestSnapshot(host, "opencode-http", 3)
	perInvocation := 600 * time.Second
	// A cohort with no tasks admits no work; the bound falls back to one
	// per-invocation limit. The advertised worker count is ignored, so it
	// cannot shrink or extend the fallback.
	for _, tc := range []struct {
		name    string
		tasks   int
		workers int
	}{
		{"zero tasks", 0, 2},
		{"zero tasks zero workers", 0, 0},
		{"negative tasks", -3, 2},
		{"negative tasks negative workers", -3, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := graphWriterCohortTimeout(s, tc.tasks, tc.workers); got != perInvocation {
				t.Fatalf("empty cohort shape must fall back to one per-invocation bound: got %s want %s", got, perInvocation)
			}
		})
	}
}

func TestGraphWriterCohortTimeoutPreservesNonOpenCodeLegacy(t *testing.T) {
	host := &config.OpenCodeHost{Version: 1, InvocationTimeoutSeconds: 600}
	for _, tc := range []struct {
		name    string
		runtime string
		version int
	}{
		{"codex isolated", "codex-app-server", 1},
		{"codex staged", "codex-app-server", 3},
		{"fake parallel", "fake", 0},
		{"opencode without isolation", "opencode-http", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := deadlineTestSnapshot(host, tc.runtime, tc.version)
			if _, ok := effectiveGraphWriterPerInvocationTimeout(s); ok {
				t.Fatal("non-OpenCode cohort claimed a derived OpenCode deadline")
			}
			for _, shape := range [][2]int{{1, 1}, {2, 2}, {8, 1}} {
				if got := graphWriterCohortTimeout(s, shape[0], shape[1]); got != legacyGraphWriterCohortTimeout {
					t.Fatalf("legacy cohort timeout changed for %s %dx%d: got %s want %s", tc.name, shape[0], shape[1], got, legacyGraphWriterCohortTimeout)
				}
			}
		})
	}
}

// TestGraphWriterCohortFailsClosedOnUnreadableController proves the cohort
// loop performs its authoritative controller read before starting any pump:
// with no readable journal the call returns the read error directly instead
// of running under a silent legacy fallback limit.
func TestGraphWriterCohortFailsClosedOnUnreadableController(t *testing.T) {
	controllerPath := filepath.Join(t.TempDir(), "missing-controller.jsonl")
	schedulePath := filepath.Join(t.TempDir(), "schedule.jsonl")
	specs := []taskscheduler.TaskSpec{{
		ID:             "fail-closed-writer",
		RunID:          strings.Repeat("a", 64),
		ControllerPath: controllerPath,
		Operation:      taskscheduler.OperationWriter,
		InvocationID:   strings.Repeat("b", 64),
	}}
	start := time.Now()
	err := runScheduledGraphWriterCohort(context.Background(), controllerPath, schedulePath, specs, 1, false)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("unreadable controller did not fail closed")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("fail-closed read waited instead of returning directly: %s", elapsed)
	}
}

// TestGraphWriterCohortCallerCancellationWinsOnCohortPath exercises the
// actual cohort control path with a minimal durable fixture: a readable
// controller journal plus a bound writer schedule. An already-cancelled
// caller context must stop the cohort loop with context.Canceled, proving
// the derived serial bound never extends caller cancellation.
func TestGraphWriterCohortCallerCancellationWinsOnCohortPath(t *testing.T) {
	root := t.TempDir()
	controllerPath := filepath.Join(root, "controller.jsonl")
	if err := Append(controllerPath, "run.created", creation(t)); err != nil {
		t.Fatal(err)
	}
	schedulePath := filepath.Join(root, "schedule.jsonl")
	specs := []taskscheduler.TaskSpec{{
		ID:             "cancelled-cohort-writer",
		RunID:          strings.Repeat("a", 64),
		ControllerPath: controllerPath,
		Operation:      taskscheduler.OperationWriter,
		InvocationID:   strings.Repeat("b", 64),
	}}
	if _, err := taskscheduler.Bind(schedulePath, taskscheduler.Definition{Version: 1, Nonce: "cohort-cancellation", Tasks: specs}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := runScheduledGraphWriterCohort(ctx, controllerPath, schedulePath, specs, 1, false)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cohort path did not preserve caller cancellation: %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("cancelled cohort path waited instead of stopping: %s", elapsed)
	}
}

// TestGraphWriterCohortCallerDeadlineHelperScope covers only the
// per-invocation runtime helper: a configured 600s invocation never extends
// an earlier caller deadline and preserves caller cancellation. It does not
// exercise the cohort loop; cohort-path cancellation is covered by
// TestGraphWriterCohortCallerCancellationWinsOnCohortPath above.
func TestGraphWriterCohortCallerDeadlineHelperScope(t *testing.T) {
	host := &config.OpenCodeHost{Version: 1, InvocationTimeoutSeconds: 600}
	callerDeadline := time.Now().Add(time.Minute)
	parent, parentCancel := context.WithDeadline(context.Background(), callerDeadline)
	defer parentCancel()
	ctx, cancel := openCodeProviderRuntimeContext(parent, host)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.Equal(callerDeadline) {
		t.Fatalf("configured 600s invocation extended caller deadline: got=%s want=%s", deadline, callerDeadline)
	}
	canceledParent, cancelParent := context.WithCancel(context.Background())
	canceled, cancelRuntime := openCodeProviderRuntimeContext(canceledParent, host)
	cancelParent()
	defer cancelRuntime()
	select {
	case <-canceled.Done():
		if !errors.Is(canceled.Err(), context.Canceled) {
			t.Fatalf("configured runtime changed caller cancellation: %v", canceled.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("configured runtime did not preserve caller cancellation")
	}
	// The cohort timeout is a duration only and never installs a context that
	// could extend the caller: the cohort loop's ctx.Done() branch retains
	// authority, so a 60-minute derived leaves-wave bound still stops on the
	// caller's earlier cancellation.
	s := deadlineTestSnapshot(host, "opencode-http", 3)
	if got := graphWriterCohortTimeout(s, 2, 2); got != 60*time.Minute {
		t.Fatalf("leaves cohort bound changed: got %s want 1h0m0s", got)
	}
}
