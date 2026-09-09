package control

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
)

func TestExplorationAdmissionAndWriterContext(t *testing.T) {
	c := creation(t)
	c.Config.Explorer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-explorer", Effort: "low", Role: "explorer"}
	c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-writer", Effort: "high", Role: "writer"}
	path, _ := approvedRepositoryCreation(t, c)
	s, err := StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	question := "Explain the fixture source and identify uncertainty."
	i, err := PrepareExplorerInvocation(path, question)
	if err != nil || i.Profile != *c.Config.Explorer {
		t.Fatal("explicit explorer routing missing", err)
	}
	beforeWriter, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	observation := Exploration{id, strings.Repeat("Observație. ", 60), []string{"file.txt"}}
	makeRecord := func(o Exploration) ExplorerRecord {
		body, err := canonical.Bytes(o)
		if err != nil {
			t.Fatal(err)
		}
		model := i.Profile.Model
		return ExplorerRecord{question, i, runtime.Result{Version: 1, InvocationID: i.ID, Requested: i.Profile, ObservedModel: &model, Output: string(body)}}
	}
	for _, bad := range []Exploration{
		{strings.Repeat("0", 64), "wrong candidate", []string{}},
		{id, "", []string{}},
		{id, "bad path", []string{"../escape"}},
		{id, "duplicate", []string{"file.txt", "file.txt"}},
		{id, "unsorted", []string{"z", "a"}},
	} {
		if _, err := RecordExploration(path, makeRecord(bad)); err == nil {
			t.Fatal("invalid exploration admitted")
		}
	}
	drift := filepath.Join(s.Workspace.Request.Path, "drift.txt")
	if err := os.WriteFile(drift, []byte("drift"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordExploration(path, makeRecord(observation)); err == nil {
		t.Fatal("live candidate drift admitted")
	}
	if err := os.Remove(drift); err != nil {
		t.Fatal(err)
	}
	s, err = RecordExploration(path, makeRecord(observation))
	if err != nil || len(s.Explorations) != 1 || s.State != "IMPLEMENTING" {
		t.Fatal("exploration admission changed workflow authority", err)
	}
	if _, err := RecordExploration(path, makeRecord(observation)); err == nil {
		t.Fatal("duplicate exploration admitted")
	}
	writer, err := PrepareWriterInvocation(path)
	if err != nil || writer.ID == beforeWriter.ID {
		t.Fatal("exploration absent from writer identity", err)
	}
	var input struct {
		Exploration explorationContext `json:"exploration"`
	}
	if err := json.Unmarshal([]byte(writer.Input), &input); err != nil {
		t.Fatal(err)
	}
	if len(input.Exploration.Records) != 1 || !input.Exploration.Records[0].Current || !input.Exploration.Records[0].Shortened || input.Exploration.Records[0].InvocationID != i.ID {
		t.Fatal("exploration projection lost provenance")
	}
	changed := s
	changed.Explorations = append([]ExplorerRecord{}, s.Explorations...)
	observation.Summary += "different omitted tail"
	changed.Explorations[0] = makeRecord(observation)
	other, err := writerInvocation(changed)
	if err != nil || other.ID == writer.ID {
		t.Fatal("omitted evidence tail not bound to invocation", err)
	}
	withoutProfile := s
	withoutProfile.Creation.Config.Explorer = nil
	if _, err := explorerInvocation(withoutProfile, question); err == nil {
		t.Fatal("missing explorer silently borrowed another role")
	}
	content := "Y2hhbmdlZAo="
	prepared, err := PrepareFiles(context.Background(), path, []fileeffects.Change{{Path: "new.txt", ContentBase64: &content}})
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := prepared.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	s, err = ApplyFiles(context.Background(), path, prepared, effects.Authorization{IntentID: intentID, Actor: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := writerExplorationContext(s)
	if err != nil || len(projection.Records) != 1 || projection.Records[0].Current || projection.Records[0].Observation.CandidateID != id {
		t.Fatal("historical exploration presented as current", err)
	}
	if _, err := RecordExploration(path, makeRecord(observation)); err == nil {
		t.Fatal("old candidate exploration re-admitted")
	}
}

func TestConfiguredExplorationRecordBoundExceedsLegacyLimit(t *testing.T) {
	c := creation(t)
	c.Config = modelAccessSnapshot(t, "subscription").Creation.Config
	c.Config.Exploration = &config.ExplorationPolicy{Version: 1, MaxRecords: 20, MaxContextRecords: 8}
	command := exec.Command("git", "-C", c.Repository.Root, "init", "-q")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	if err := os.WriteFile(filepath.Join(c.Repository.Root, "file.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "file.txt"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base"}} {
		command = exec.Command("git", append([]string{"-C", c.Repository.Root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	}
	var err error
	c.Repository, err = repository.Discover(context.Background(), c.Repository.Root, c.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	planned, err := ResumePlanning(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "plan.approved", Approval{PlanID: planned.PlanID, Actor: "operator"}); err != nil {
		t.Fatal(err)
	}
	s, err := StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		question := fmt.Sprintf("bounded explorer question %02d", index)
		invocation, err := PrepareExplorerInvocation(path, question)
		if err != nil {
			t.Fatal(err)
		}
		body, err := canonical.Bytes(Exploration{CandidateID: candidateID, Summary: question, Paths: []string{}})
		if err != nil {
			t.Fatal(err)
		}
		model := invocation.Profile.Model
		record := ExplorerRecord{Question: question, Invocation: invocation, Result: runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, Output: string(body)}}
		if _, err := RecordExploration(path, record); err != nil {
			t.Fatal("configured exploration record rejected", index, err)
		}
	}
	if current, err := Inspect(path); err != nil || len(current.Explorations) != 20 {
		t.Fatal("configured exploration records were not retained", err)
	}
	extraQuestion := "configured explorer question beyond bound"
	extraInvocation, err := PrepareExplorerInvocation(path, extraQuestion)
	if err != nil {
		t.Fatal(err)
	}
	extraBody, err := canonical.Bytes(Exploration{CandidateID: candidateID, Summary: extraQuestion, Paths: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	model := extraInvocation.Profile.Model
	if _, err := RecordExploration(path, ExplorerRecord{Question: extraQuestion, Invocation: extraInvocation, Result: runtime.Result{Version: 1, InvocationID: extraInvocation.ID, Requested: extraInvocation.Profile, ObservedModel: &model, Output: string(extraBody)}}); err == nil {
		t.Fatal("configured exploration record bound exceeded")
	}
}
