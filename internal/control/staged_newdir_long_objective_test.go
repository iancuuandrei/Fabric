package control

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/engineeringplan"
)

// stagedNewDirGraph mirrors the fresh staged Muse demo shape: a hub that owns
// a NEW directory package plus dependent leaves. The hub scope/write targets a
// directory that does not exist in the pristine parent.
func stagedNewDirGraph() engineeringplan.Graph {
	return engineeringplan.Graph{Version: engineeringplan.Version, Mode: engineeringplan.ModeGraph, Summary: "Add concurrency-safe memlog package via hub record store", Tasks: []engineeringplan.Task{
		{ID: "hub-records", Kind: engineeringplan.Implementation, Title: "Hub record store memlog/records.go", ScopePaths: []string{"memlog"}, WritePaths: []string{"memlog/records.go"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "memlog/records.go defines Record Recorder"}}, EstimatedSeconds: 300},
		{ID: "sink-leaf", Kind: engineeringplan.Implementation, Title: "Sink leaf memlog/sink.go", Dependencies: []string{"hub-records"}, ScopePaths: []string{"memlog"}, WritePaths: []string{"memlog/sink.go"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "memlog/sink.go defines LogSink"}}, EstimatedSeconds: 300},
		{ID: "tests-leaf", Kind: engineeringplan.Implementation, Title: "Tests leaf memlog/memlog_test.go", Dependencies: []string{"hub-records"}, ScopePaths: []string{"memlog"}, WritePaths: []string{"memlog/memlog_test.go"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "memlog tests cover public API"}}, EstimatedSeconds: 300},
		{ID: "verification", Kind: engineeringplan.Verification, Title: "Native go test verification", Dependencies: []string{"hub-records", "sink-leaf", "tests-leaf"}, ScopePaths: []string{"memlog"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "test_log", Description: "native checks pass"}}, EstimatedSeconds: 180},
		{ID: "review", Kind: engineeringplan.Review, Title: "Review memlog package", Dependencies: []string{"hub-records", "sink-leaf", "tests-leaf", "verification"}, ScopePaths: []string{"memlog"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "review", Description: "approve memlog package"}}, EstimatedSeconds: 120},
	}}
}

// TestStagedNewDirLongObjectiveAdmitsChildContext reproduces the fresh staged
// demo blocker: a realistic long objective (snapshot run ec550511 measured
// 8962 objective bytes, 9073 question bytes) must admit the hub fork, its
// isolated writer task context, and the exact child binding offline. No
// provider calls, no live resume, no journal mutation beyond the fixture.
func TestStagedNewDirLongObjectiveAdmitsChildContext(t *testing.T) {
	c := isolatedOpenCodeCreation(t)
	c.Execution.IsolatedImplementationVersion = 3
	c.Execution.MaxParallel = 2
	c.Config.PlannerContract = plannerContractGraphV8
	c = applyTwoSlotIsolationCapacity(t, c)
	// Synthetic long objective with the same length as the staged demo
	// (8962 bytes) without copying task text into source.
	c.Objective = strings.Repeat("objective-seed ", 640)[:8962]
	if err := c.Execution.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := c.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	path, s := isolatedWriterControllerFixture(t, c, stagedNewDirGraph())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareStagedCohort(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	// The hub question exceeds the legacy 4096 writer bound (9073 bytes here)
	// but stays within the 256 KiB task-context admission bound.
	question, err := graphWriterTaskQuestion(s, "hub-records")
	if err != nil {
		t.Fatalf("hub writer question blocked normal product use: %v", err)
	}
	if len(question) <= 4096 {
		t.Fatalf("fixture did not reproduce the long-objective seam: question=%d", len(question))
	}
	writerQuestion, err := writerTaskContextQuestion(s, "hub-records")
	if err != nil {
		t.Fatalf("hub writer task-context question blocked: %v", err)
	}
	if writerQuestion != question {
		t.Fatal("writer question builders diverged for the same hub task")
	}
	bindings, err := prepareStagedBindings(context.Background(), path)
	if err != nil {
		t.Fatalf("staged bindings blocked after confirmed fork: %v", err)
	}
	if len(bindings) != 1 || bindings[0].TaskID != "hub-records" {
		t.Fatalf("staged bindings did not confirm the exact hub cohort: %+v", bindings)
	}
	after, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := stagedForkBindingForTask(after, "hub-records")
	if err != nil {
		t.Fatal(err)
	}
	if binding.TaskID != "hub-records" || len(binding.Task.WritePaths) != 1 || binding.Task.WritePaths[0] != "memlog/records.go" {
		t.Fatalf("child binding lost immutable write scope: %+v", binding.Task)
	}
	found := false
	for _, rec := range after.TaskContexts {
		if rec.IsolationTaskID == "hub-records" && rec.Role == "writer" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("hub fork confirmed but no isolated writer task context was admitted")
	}
}

// TestStagedNewDirWriterQuestionBuildersPureBounds locks the approved
// task-bound admission without any fixture: both builders return the exact
// pre-change short bytes, reject above the 256 KiB full-query bound, and
// admit a >16 KiB Unicode question whose selection view truncates on a UTF-8
// boundary with full digest and length.
func TestStagedNewDirWriterQuestionBuildersPureBounds(t *testing.T) {
	pureSnapshot := func(objective string) Snapshot {
		return Snapshot{Creation: Creation{Objective: objective}, Graph: &GraphState{Graph: stagedNewDirGraph()}}
	}
	const wantShort = "[hub-records] Hub record store memlog/records.go | scope: memlog | write paths: memlog/records.go | objective: short objective"
	gotGraph, err := graphWriterTaskQuestion(pureSnapshot("short objective"), "hub-records")
	if err != nil {
		t.Fatalf("short graph writer question rejected: %v", err)
	}
	gotWriter, err := writerTaskContextQuestion(pureSnapshot("short objective"), "hub-records")
	if err != nil {
		t.Fatalf("short writer task-context question rejected: %v", err)
	}
	if gotGraph != wantShort || gotWriter != wantShort {
		t.Fatalf("short writer bytes changed: graph=%q writer=%q want=%q", gotGraph, gotWriter, wantShort)
	}
	if len(gotGraph) > 4096 || len(gotWriter) > 4096 {
		t.Fatalf("short question left the historical 4096-byte shape: %d", len(gotGraph))
	}
	bigObjective := strings.Repeat("q", taskContextFullQueryMax+1)
	if _, err := graphWriterTaskQuestion(pureSnapshot(bigObjective), "hub-records"); err == nil {
		t.Fatal("graph writer builder admitted a question above 256 KiB")
	}
	if _, err := writerTaskContextQuestion(pureSnapshot(bigObjective), "hub-records"); err == nil {
		t.Fatal("writer task-context builder admitted a question above 256 KiB")
	}
	unicodeObjective := strings.Repeat("é", 10000)
	longGraph, err := graphWriterTaskQuestion(pureSnapshot(unicodeObjective), "hub-records")
	if err != nil {
		t.Fatalf("graph writer builder rejected >16 KiB Unicode question: %v", err)
	}
	longWriter, err := writerTaskContextQuestion(pureSnapshot(unicodeObjective), "hub-records")
	if err != nil {
		t.Fatalf("writer task-context builder rejected >16 KiB Unicode question: %v", err)
	}
	if longGraph != longWriter {
		t.Fatal("writer question builders diverged on the Unicode question")
	}
	if len(longGraph) <= taskContextQueryCap {
		t.Fatalf("Unicode fixture did not exceed the 16 KiB selection cap: %d", len(longGraph))
	}
	bound, truncated, hash, qlen := truncateTaskQuery(longGraph)
	if !truncated {
		t.Fatal("long Unicode question was not marked truncated")
	}
	if len(bound) > taskContextQueryCap || !utf8.ValidString(bound) {
		t.Fatalf("truncated selection is not valid UTF-8 within 16 KiB: %d", len(bound))
	}
	if hash != taskContextQueryHash(longGraph) || qlen != len(longGraph) {
		t.Fatal("truncated selection lost the full digest or length")
	}
	mutated := longGraph[:len(longGraph)-2] + "Z"
	boundAfter, truncatedAfter, hashAfter, lenAfter := truncateTaskQuery(mutated)
	if !truncatedAfter {
		t.Fatal("mutated long question was not marked truncated")
	}
	if boundAfter != bound {
		t.Fatal("bound prefix changed when only the suffix after the cut changed")
	}
	if hashAfter == hash {
		t.Fatal("full digest did not change when the suffix after the cut changed")
	}
	if hashAfter != taskContextQueryHash(mutated) || lenAfter != len(mutated) {
		t.Fatal("mutated truncation lost the full digest or length")
	}
}
