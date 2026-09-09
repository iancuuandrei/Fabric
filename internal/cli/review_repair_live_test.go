package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/fileeffects"
)

func seedNegativeReview(t *testing.T, ctx context.Context, root string, snapshot *control.Snapshot) {
	t.Helper()
	path, err := runPath(root, snapshot.RunID)
	if err != nil {
		t.Fatal(err)
	}
	content := base64.StdEncoding.EncodeToString([]byte("incorrect\n"))
	prepared, err := control.PrepareFiles(ctx, path, []fileeffects.Change{{Path: "generated.txt", ContentBase64: &content}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := prepared.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	s, err := control.ApplyFiles(ctx, path, prepared, effects.Authorization{IntentID: id, Actor: "fixture-operator"})
	if err != nil {
		t.Fatal(err)
	}
	s, err = control.Verify(ctx, path)
	if err != nil || s.State != "REVIEWING" {
		t.Fatal("negative review fixture did not reach review", err)
	}
	before := *s.Candidate
	record, err := control.RunReview(ctx, path)
	if err != nil {
		t.Fatal("negative reviewer execution failed", err)
	}
	var verdict control.ReviewVerdict
	if err := json.Unmarshal([]byte(record.Result.Output), &verdict); err != nil {
		t.Fatal(err)
	}
	s, err = control.Inspect(path)
	if err != nil || s.State != "REPAIRING" || *s.Candidate != before || s.ReviewHost == nil || s.ReviewHost.RuntimeReceipt == nil || verdict.Decision != "changes_requested" || len(verdict.Findings) == 0 {
		t.Fatal("reviewer did not reject incorrect candidate with durable evidence", err)
	}
	matched := false
	for _, finding := range verdict.Findings {
		matched = matched || finding.Path == "generated.txt"
	}
	if !matched {
		t.Fatal("reviewer did not identify the incorrect file")
	}
	*snapshot = s
}
