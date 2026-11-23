package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/evidencevalue"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
)

func TestEvidenceFeedbackScheduleArgumentStrict(t *testing.T) {
	root := t.TempDir()
	runID := strings.Repeat("a", 64)
	scheduleID := strings.Repeat("b", 64)
	file := filepath.Join(root, "feedback.json")
	if err := os.WriteFile(file, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{},
		{runID},
		{runID, file, "--schedule"},
		{runID, file, "--schedule", ""},
		{runID, file, "--schedule", scheduleID, "extra"},
		{runID, file, "--bogus", scheduleID},
		{runID, file, scheduleID},
	} {
		var denied bytes.Buffer
		if err := evidenceFeedbackCommand(root, args, &denied); err == nil {
			t.Fatalf("invalid evidence-feedback arguments admitted: %q", args)
		} else if denied.Len() != 0 {
			t.Fatalf("rejected evidence-feedback emitted output: %q", args)
		}
	}
}

func evidenceFeedbackScheduleFixture(t *testing.T) (root string, run func(...string) []byte, first, second control.Snapshot) {
	t.Helper()
	root = t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatal(err, string(out))
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("feedback schedule fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	run = func(args ...string) []byte {
		t.Helper()
		var out bytes.Buffer
		if err := Execute(context.Background(), args, root, &out); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	run("init")
	path := filepath.Join(root, "harness.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "version = 1", "version = 2", 1) + `
[access]
class = "PRIVATE"
[access.limits]
tokens = 1000
concurrency = 1
[access.roles]
planner = "fixture"
[access.invocations.planner]
tokens = 200
[[access.profiles]]
version = 1
name = "fixture"
kind = "subscription"
runtime = "fake"
provider = "deterministic"
auth_mode = "fixture"
repository_classes = ["PRIVATE"]
`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	run("doctor")
	if err := json.Unmarshal(run("plan", "First fixture objective"), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(run("plan", "Second fixture objective"), &second); err != nil {
		t.Fatal(err)
	}
	if first.RunID == second.RunID {
		t.Fatal("fixture plans must create distinct runs")
	}
	return root, run, first, second
}

func writeFeedbackRequest(t *testing.T, root string, s control.Snapshot) string {
	t.Helper()
	path, err := runPath(root, s.RunID)
	if err != nil {
		t.Fatal(err)
	}
	state, head, err := control.InspectWithHead(path)
	if err != nil {
		t.Fatal(err)
	}
	source, err := state.Creation.Repository.ID()
	if err != nil {
		t.Fatal(err)
	}
	candidate := ""
	if state.Candidate != nil {
		candidate, err = state.Candidate.ID()
		if err != nil {
			t.Fatal(err)
		}
	}
	model := evidencevalue.Request{Version: 1, Binding: evidencevalue.Binding{RunID: s.RunID, SourceID: source, CandidateID: candidate, JournalHead: head}, Resources: []evidencevalue.Resource{{Name: "wall_ms", Limit: 100}}}
	feedback := evidencevalue.FeedbackRequest{Model: evidencevalue.EncodeRequest(model), Allocations: map[string]evidencevalue.Numeric{"wall_ms": "100"}, Consumed: map[string][]string{}}
	raw, err := canonical.Bytes(feedback)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "feedback.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func writeScheduleDefinition(t *testing.T, root, name, runID, controllerPath string) string {
	t.Helper()
	rawDefinition, err := json.Marshal(taskscheduler.Definition{Version: 1, Nonce: name, Tasks: []taskscheduler.TaskSpec{{ID: "t1", RunID: runID, ControllerPath: controllerPath, Operation: taskscheduler.OperationPlanner, InvocationID: strings.Repeat("1", 64)}}})
	if err != nil {
		t.Fatal(err)
	}
	definitionPath := filepath.Join(root, name+".json")
	if err := os.WriteFile(definitionPath, rawDefinition, 0600); err != nil {
		t.Fatal(err)
	}
	return definitionPath
}

func TestEvidenceFeedbackScheduleMustReferenceSelectedRun(t *testing.T) {
	root, run, first, second := evidenceFeedbackScheduleFixture(t)
	firstPath := filepath.Join(root, ".harness", "runs", first.RunID+".jsonl")
	secondPath := filepath.Join(root, ".harness", "runs", second.RunID+".jsonl")
	var otherSchedule taskscheduler.Snapshot
	if err := json.Unmarshal(run("schedule-create", writeScheduleDefinition(t, root, "other-feedback-schedule", second.RunID, secondPath)), &otherSchedule); err != nil {
		t.Fatal(err)
	}
	file := writeFeedbackRequest(t, root, first)
	before, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	var mismatched bytes.Buffer
	mismatchErr := Execute(context.Background(), []string{"evidence-feedback", first.RunID, file, "--schedule", otherSchedule.ScheduleID}, root, &mismatched)
	if mismatchErr == nil || !strings.Contains(mismatchErr.Error(), "does not bind the selected run") {
		t.Fatalf("same-repository different-run schedule admitted, got %v", mismatchErr)
	}
	if mismatched.Len() != 0 {
		t.Fatal("rejected feedback emitted output before validation")
	}
	after, err := os.ReadFile(firstPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected feedback mutated the run journal")
	}
}

func TestEvidenceFeedbackScheduleThreadsSchedulerForOwnRun(t *testing.T) {
	root, run, first, _ := evidenceFeedbackScheduleFixture(t)
	firstPath := filepath.Join(root, ".harness", "runs", first.RunID+".jsonl")
	var ownSchedule taskscheduler.Snapshot
	if err := json.Unmarshal(run("schedule-create", writeScheduleDefinition(t, root, "own-feedback-schedule", first.RunID, firstPath)), &ownSchedule); err != nil {
		t.Fatal(err)
	}
	file := writeFeedbackRequest(t, root, first)
	before, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	var priced control.EvidenceFeedbackReport
	if err := json.Unmarshal(run("evidence-feedback", first.RunID, file, "--schedule", ownSchedule.ScheduleID), &priced); err != nil {
		t.Fatal("valid schedule must preserve feedback", err)
	}
	if priced.UsageHash == "" || len(priced.Observations) != 0 || priced.Scope != "receipt_matched_completed_codex_typed_tokens" {
		t.Fatal("fake work with scheduler acquired observed costs", priced)
	}
	after, err := os.ReadFile(firstPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("scheduled feedback mutated the run journal")
	}
	var denied bytes.Buffer
	deniedErr := Execute(context.Background(), []string{"evidence-feedback", first.RunID, file, "--schedule", strings.Repeat("0", 64)}, root, &denied)
	if deniedErr == nil {
		t.Fatal("mismatched schedule admitted for feedback")
	}
	if denied.Len() != 0 {
		t.Fatal("failed feedback emitted output before validation")
	}
}
