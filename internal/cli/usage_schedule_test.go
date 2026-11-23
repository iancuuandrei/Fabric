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

	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
)

func TestUsageScheduleArgument(t *testing.T) {
	runID := strings.Repeat("a", 64)
	scheduleID := strings.Repeat("b", 64)
	gotRun, gotSchedule, err := usageScheduleArgument([]string{runID})
	if err != nil || gotRun != runID || gotSchedule != "" {
		t.Fatal("plain RUN must parse without scheduler", gotRun, gotSchedule, err)
	}
	gotRun, gotSchedule, err = usageScheduleArgument([]string{runID, "--schedule", scheduleID})
	if err != nil || gotRun != runID || gotSchedule != scheduleID {
		t.Fatal("RUN --schedule must parse both identities", gotRun, gotSchedule, err)
	}
	for _, args := range [][]string{
		{},
		{runID, "--schedule"},
		{runID, "--schedule", scheduleID, "extra"},
		{runID, "--bogus", scheduleID},
		{runID, scheduleID},
	} {
		if _, _, err := usageScheduleArgument(args); err == nil {
			t.Fatalf("invalid usage arguments admitted: %q", args)
		}
	}
}

func TestUsageScheduleBinding(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatal(err, string(out))
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("schedule fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	run := func(args ...string) []byte {
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
	var s control.Snapshot
	if err := json.Unmarshal(run("plan", "Assess this fixture"), &s); err != nil {
		t.Fatal(err)
	}
	run("resume", s.RunID)
	var usage control.RunUsage
	if err := json.Unmarshal(run("usage", s.RunID), &usage); err != nil {
		t.Fatal(err)
	}
	if usage.RunID != s.RunID {
		t.Fatal("plain usage must report the selected run", usage.RunID)
	}
	var denied bytes.Buffer
	deniedErr := Execute(context.Background(), []string{"usage", s.RunID, "--schedule", strings.Repeat("0", 64)}, root, &denied)
	if deniedErr == nil {
		t.Fatal("mismatched schedule admitted for usage")
	}
	if denied.Len() != 0 {
		t.Fatal("failed usage emitted output before validation")
	}
	var badFlag bytes.Buffer
	if err := Execute(context.Background(), []string{"usage", s.RunID, "--bogus", strings.Repeat("0", 64)}, root, &badFlag); err == nil {
		t.Fatal("unknown usage flag admitted")
	}
	if badFlag.Len() != 0 {
		t.Fatal("failed usage emitted output before validation")
	}
	if !strings.Contains(Reference(), "--schedule") {
		t.Fatal("generated CLI reference omits the usage schedule binding")
	}
}

func TestUsageScheduleMustReferenceSelectedRun(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatal(err, string(out))
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("schedule run binding"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	run := func(args ...string) []byte {
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
	var first control.Snapshot
	if err := json.Unmarshal(run("plan", "First fixture objective"), &first); err != nil {
		t.Fatal(err)
	}
	var second control.Snapshot
	if err := json.Unmarshal(run("plan", "Second fixture objective"), &second); err != nil {
		t.Fatal(err)
	}
	if first.RunID == second.RunID {
		t.Fatal("fixture plans must create distinct runs")
	}
	firstPath := filepath.Join(root, ".harness", "runs", first.RunID+".jsonl")
	secondPath := filepath.Join(root, ".harness", "runs", second.RunID+".jsonl")
	writeDefinition := func(name, runID, controllerPath string) string {
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
	var otherSchedule taskscheduler.Snapshot
	if err := json.Unmarshal(run("schedule-create", writeDefinition("other-schedule", second.RunID, secondPath)), &otherSchedule); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	var mismatched bytes.Buffer
	mismatchErr := Execute(context.Background(), []string{"usage", first.RunID, "--schedule", otherSchedule.ScheduleID}, root, &mismatched)
	if mismatchErr == nil || !strings.Contains(mismatchErr.Error(), "does not bind the selected run") {
		t.Fatalf("same-repository different-run schedule admitted, got %v", mismatchErr)
	}
	if mismatched.Len() != 0 {
		t.Fatal("rejected usage emitted output before validation")
	}
	after, err := os.ReadFile(firstPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected usage mutated the run journal")
	}
	var ownSchedule taskscheduler.Snapshot
	if err := json.Unmarshal(run("schedule-create", writeDefinition("own-schedule", first.RunID, firstPath)), &ownSchedule); err != nil {
		t.Fatal(err)
	}
	var usage control.RunUsage
	if err := json.Unmarshal(run("usage", first.RunID, "--schedule", ownSchedule.ScheduleID), &usage); err != nil {
		t.Fatal("valid schedule must preserve usage", err)
	}
	if usage.RunID != first.RunID {
		t.Fatal("scheduled usage must report the selected run", usage.RunID)
	}
}
