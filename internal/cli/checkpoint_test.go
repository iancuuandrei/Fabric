package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/repository"
)

func TestCheckpointCommandEmitsOnlyBoundSummary(t *testing.T) {
	root := t.TempDir()
	configBytes := []byte(config.Example)
	if err := os.WriteFile(filepath.Join(root, "harness.toml"), configBytes, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(configBytes)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Repository = "fixture"
	creation := control.Creation{
		Version: 1, Nonce: "checkpoint-cli", Objective: "DO NOT EXPOSE OBJECTIVE BODY",
		Repository: repository.Identity{Version: 1, Name: "fixture", Root: root, CommonDir: filepath.Join(root, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)},
		Config:     cfg,
	}
	runID, err := canonical.Hash("harness.run.v1", creation)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".harness", "runs", runID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := control.Append(path, "run.created", creation); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := checkpointCommand(context.Background(), root, []string{runID}, &output); err != nil {
		t.Fatal(err)
	}
	var got control.RunCheckpoint
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.RunID != runID || got.JournalHead == "" || got.AcceptedCheckpoint || got.State != "OBJECTIVE" {
		t.Fatalf("unexpected checkpoint: %+v", got)
	}
	if strings.Contains(output.String(), "DO NOT EXPOSE") || strings.Contains(output.String(), "verification") && strings.Contains(output.String(), "Excerpt") {
		t.Fatalf("checkpoint output exposed prompt or process output: %s", output.String())
	}
}

func TestCheckpointCommandRequiresOneRunIDAndHonorsCancellation(t *testing.T) {
	var output bytes.Buffer
	if err := checkpointCommand(context.Background(), t.TempDir(), nil, &output); err == nil || output.Len() != 0 {
		t.Fatal("missing ID accepted or output written", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkpointCommand(ctx, t.TempDir(), []string{strings.Repeat("a", 64)}, &output); err == nil || output.Len() != 0 {
		t.Fatal("cancelled checkpoint read succeeded", err)
	}
}
