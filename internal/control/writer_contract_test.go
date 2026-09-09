package control

import (
	"context"
	"encoding/json"
	"errors"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/writercontract"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriterEmptyProposalDoesNotAdvanceWorkflow(t *testing.T) {
	c := creation(t)
	c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "writer", Effort: "medium", Role: "writer"}
	c.Config.WriterContract = "nonempty-v1"
	path, _ := approvedRepositoryCreation(t, c)
	s, err := StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	i, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Instruction string          `json:"instruction"`
		Schema      json.RawMessage `json:"output_schema"`
	}
	if err := json.Unmarshal([]byte(i.Input), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Schema) == 0 || !strings.Contains(envelope.Instruction, "Produce the implementation") || !strings.Contains(envelope.Instruction, "1 to 64") {
		t.Fatal("writer mutation contract missing")
	}
	id, _ := s.Candidate.ID()
	base, err := os.ReadFile(filepath.Join(s.Workspace.Request.Path, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 65} {
		changes := []fileeffects.Change{}
		for k := 0; k < n; k++ {
			changes = append(changes, fileeffects.Change{Path: "x", ContentBase64: content64("x")})
		}
		b, _ := canonical.Bytes(WriterProposal{id, changes})
		r := runtime.Result{Version: 1, InvocationID: i.ID, Requested: i.Profile, Output: string(b)}
		_, err := RecordWriterProposal(context.Background(), path, i, r)
		if n == 0 && !errors.Is(err, writercontract.ErrEmptyChangeset) {
			t.Fatal("empty classification", err)
		}
		if n == 65 && !errors.Is(err, writercontract.ErrTooManyChanges) {
			t.Fatal("large classification", err)
		}
		after, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		next, nextErr := PrepareWriterInvocation(path)
		if nextErr != nil || next.Profile.Role != "writer" {
			t.Fatal("invalid proposal selected fixer", nextErr)
		}
		if after.State != "IMPLEMENTING" || after.WriterProposal != nil || after.Verification != nil || after.Review != nil || after.Commit != nil || after.Candidate == nil || *after.Candidate != *s.Candidate {
			t.Fatal("invalid writer advanced workflow")
		}
		current, _ := os.ReadFile(filepath.Join(s.Workspace.Request.Path, "file.txt"))
		if string(current) != string(base) {
			t.Fatal("candidate mutated")
		}
	}
}
