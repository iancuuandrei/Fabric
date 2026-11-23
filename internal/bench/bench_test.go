package bench

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/agentloop"
	"github.com/iancuuandrei/fabric/internal/cost"
)

type benchStubModel struct {
	name string
}

func (m *benchStubModel) Name() string { return m.name }
func (m *benchStubModel) Complete(_ context.Context, _ string, _ []agentloop.Message, _ []agentloop.ToolDef) (agentloop.ModelResponse, error) {
	return agentloop.ModelResponse{
		Text:  "completed benchmark task",
		Usage: cost.Usage{InputTokens: 1000, CachedTokens: 800, OutputTokens: 100},
	}, nil
}

func TestLoadSuiteAndRunScorecard(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=T", "-c", "user.email=t@e.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	suitePath := filepath.Join(t.TempDir(), "suite.jsonl")
	raw := strings.Join([]string{
		"# sample suite",
		`{"id":"t1","objective":"Verify one"}`,
		`{"id":"t2","objective":"Verify two","verify_argv":["git","--version"]}`,
	}, "\n")
	if err := os.WriteFile(suitePath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	tasks, err := LoadSuite(suitePath)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("LoadSuite failed: tasks=%+v err=%v", tasks, err)
	}
	b, _ := json.Marshal(tasks)
	_ = b

	sum, err := Run(context.Background(), Config{
		RepoRoot: dir,
		Model:    &benchStubModel{name: "claude-sonnet-4-5"},
		Tasks:    tasks,
	})
	if err != nil {
		t.Fatalf("bench.Run failed: %v", err)
	}
	if sum.Total != 2 || sum.Passed != 2 || sum.PassRate != 1.0 || sum.MeanCacheHit < 0.79 {
		t.Fatalf("unexpected benchmark summary: %+v", sum)
	}
	md := sum.Markdown()
	if !strings.Contains(md, "Fabric Benchmark Scorecard") || !strings.Contains(md, "100.0%") {
		t.Fatalf("unexpected markdown scorecard:\n%s", md)
	}
}
