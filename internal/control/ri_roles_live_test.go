package control

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"harness.local/engorch/internal/codexruntime"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/journal"
)

func qualifyLiveRIRoles(t *testing.T, path, store string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	intent, err := PrepareRIPublish(path, store)
	if err != nil {
		t.Fatal(err)
	}
	id, err := intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	s, err := ExecuteRIPublish(ctx, path, store, effects.Authorization{IntentID: id, Actor: "fixture-operator"})
	if err != nil || s.RIPublish.Outcome != "CONFIRMED" {
		t.Fatal("RI publication failed", err)
	}
	binding, err := SelectRuntimeRI(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := RunWriter(ctx, path)
	if err != nil {
		t.Fatal("RI writer failed", err)
	}
	if _, err := os.Stat(filepath.Join(s.Workspace.Request.Path, "generated.txt")); !os.IsNotExist(err) {
		t.Fatal("writer mutated candidate without file approval", err)
	}
	id, err = proposal.Prepared.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	s, err = ApplyFiles(ctx, path, proposal.Prepared, effects.Authorization{IntentID: id, Actor: "fixture-operator"})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(s.Workspace.Request.Path, "generated.txt"))
	if err != nil || string(content) != "salut\n" {
		t.Fatal("RI-guided proposal content mismatch", err)
	}
	s, err = Verify(ctx, path)
	if err != nil || s.State != "REVIEWING" {
		t.Fatal("review gate missing", err)
	}
	before := *s.Candidate
	if _, err := RunReview(ctx, path); err != nil {
		t.Fatal("RI review failed", err)
	}
	s, err = Inspect(path)
	if err != nil || s.State != "READY" || *s.Candidate != before {
		t.Fatal("review failed to admit unchanged candidate", err)
	}
	for _, role := range []struct{ name, root, head string }{
		{"writer", s.WriterHost.Intent.Launch.Root, s.WriterHost.RuntimeReceipt.JournalHead},
		{"review", s.ReviewHost.Intent.Launch.Root, s.ReviewHost.RuntimeReceipt.JournalHead},
	} {
		runtimePath := filepath.Join(role.root, role.name+".jsonl")
		state, head, err := codexruntime.InspectWithHead(runtimePath)
		if err != nil || head != role.head || state.RI == nil || *state.RI != binding || state.PendingTool != nil {
			t.Fatal(role.name, "runtime binding/receipt mismatch", err)
		}
		events, err := journal.Read(runtimePath)
		if err != nil {
			t.Fatal(err)
		}
		calls := map[string]string{}
		for _, event := range events {
			if event.Kind == "runtime.tool-request" {
				var request codexruntime.ToolRequest
				if err := json.Unmarshal(event.Payload, &request); err != nil {
					t.Fatal(err)
				}
				calls[request.CallID] = request.Tool
			}
		}
		succeeded := map[string]bool{}
		for _, response := range state.ToolResponses {
			if response.Success {
				succeeded[calls[response.CallID]] = true
			}
		}
		for _, tool := range []string{"ri_status", "ri_locate", "ri_definition"} {
			if !succeeded[tool] {
				t.Fatalf("%s did not successfully use %s", role.name, tool)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(s.Creation.Repository.Root, "generated.txt")); !os.IsNotExist(err) {
		t.Fatal("canonical source changed", err)
	}
}
