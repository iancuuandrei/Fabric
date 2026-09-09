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

	"harness.local/engorch/internal/control"
)

func TestV2AccessThroughCLI(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatal(err, string(out))
		}
		return string(out)
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("unchanged fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	head := git("rev-parse", "HEAD")
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
	if s.State != "AWAITING_APPROVAL" || s.PlannerAccess == nil || s.PlannerAccess.Reservation.Tokens != 200 {
		t.Fatal("CLI bypassed access admission")
	}
	journalPath := filepath.Join(root, ".harness", "runs", s.RunID+".jsonl")
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	run("resume", s.RunID)
	var usage control.RunUsage
	if err := json.Unmarshal(run("usage", s.RunID), &usage); err != nil {
		t.Fatal(err)
	}
	if len(usage.Admissions) != 1 || usage.Admissions[0].Receipt == nil || usage.Admissions[0].Receipt.CostMicroUSD != nil {
		t.Fatal("CLI accounting missing or fabricated")
	}
	after, err := os.ReadFile(journalPath)
	if err != nil || !bytes.Equal(before, after) || git("rev-parse", "HEAD") != head {
		t.Fatal("readback mutated run or repository", err)
	}
	// A new plan under public-only access must reject this private repository.
	bad := strings.Replace(text, "repository_classes = [\"PRIVATE\"]", "repository_classes = [\"PUBLIC\"]", 1)
	if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if Execute(context.Background(), []string{"plan", "Must be denied"}, root, &out) == nil {
		t.Fatal("privacy mismatch admitted by CLI")
	}
}
