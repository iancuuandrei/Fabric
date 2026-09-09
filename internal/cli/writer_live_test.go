package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"harness.local/engorch/internal/codexhost"
	"harness.local/engorch/internal/codexruntime"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
)

func TestLiveCodexWriterCLI(t *testing.T) {
	liveCodexWriter(t, false, false, false, false)
}

func TestLiveCodexCandidateWriterCLI(t *testing.T) {
	liveCodexWriter(t, true, false, false, false)
}

func TestLiveCodexRepairCLI(t *testing.T) {
	liveCodexWriter(t, false, true, false, false)
}

func TestLiveCodexReviewCLI(t *testing.T) {
	liveCodexWriter(t, false, true, true, false)
}

func TestLiveCodexNegativeReviewRepairCLI(t *testing.T) {
	liveCodexWriter(t, false, false, true, true)
}

func TestLiveCodexExplorerWriterCLI(t *testing.T) {
	liveCodexWriter(t, true, false, false, false, true)
}

func TestWriterRepairCheckHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != "engorch-writer-repair-check" {
		return
	}
	content, err := os.ReadFile("generated.txt")
	if err != nil || string(content) != "writer\n" {
		fmt.Fprintln(os.Stderr, "generated.txt must contain exactly writer followed by a newline; create or correct it.")
		os.Exit(1)
	}
	os.Exit(0)
}

func liveCodexWriter(t *testing.T, candidateTools, repair, review, negativeReview bool, exploration ...bool) {
	explore := len(exploration) == 1 && exploration[0]
	binary, auth := os.Getenv("ENGORCH_CODEX_PROBE_BINARY"), os.Getenv("ENGORCH_CODEX_AUTH_SOURCE")
	if binary == "" || auth == "" {
		t.Skip("opt-in authenticated writer qualification")
	}
	base := t.TempDir()
	if evidence := os.Getenv("ENGORCH_CODEX_CLI_EVIDENCE"); evidence != "" {
		var err error
		base, err = os.MkdirTemp(filepath.Dir(evidence), "codex-writer-attempt-")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("retained writer attempt: %s", base)
	}
	root, stateRoot := filepath.Join(base, "source"), filepath.Join(base, "state")
	for _, p := range []string{root, stateRoot} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	probe, err := codexhost.Prepare(t.TempDir(), binary)
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Parse([]byte(config.Example))
	if err != nil {
		t.Fatal(err)
	}
	c.Writer = &runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "gpt-5.6-luna", Effort: "low", Role: "writer"}
	if explore {
		c.Explorer = &runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "gpt-5.6-luna", Effort: "low", Role: "explorer"}
	}
	if review {
		c.Reviewer = &runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "gpt-5.6-luna", Effort: "low", Role: "reviewer"}
	}
	if repair {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		c.Verification = []config.Check{{Name: "generated-content", Argv: []string{executable, "-test.run=^TestWriterRepairCheckHelper$", "--", "engorch-writer-repair-check"}, TimeoutSeconds: 20}}
	}
	if negativeReview {
		c.Verification = []config.Check{{Name: "git-version", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}}
	}
	c.Codex = &config.Codex{Executable: binary, ExecutableHash: probe.BinaryHash, StateRoot: stateRoot, AuthSource: auth}
	encoded, err := toml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "harness.toml"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	run := func(target any, args ...string) {
		t.Helper()
		var out bytes.Buffer
		if err := Execute(ctx, args, root, &out); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(out.Bytes(), target); err != nil {
			t.Fatal(err)
		}
	}
	var s control.Snapshot
	expected := "writer\n"
	prompt := "Create only generated.txt containing exactly writer followed by a newline. Its content_base64 is d3JpdGVyCg==, before_hash is null, executable is false. Return the required proposal JSON with the exact candidate_id supplied by the controller; do not modify files or run tools."
	if candidateTools {
		prompt = "Create only generated.txt with the exact bytes currently in source.txt in the admitted candidate. Use candidate_list to discover the file and candidate_read to obtain its content_base64, then copy that exact base64 into the proposal. Do not use base-commit bytes: source.txt has an admitted modification. generated.txt is absent, so before_hash is null and executable is false. Return strict proposal JSON; do not mutate files."
	}
	if repair {
		prompt = "Repair the failing configured verification using the controller-provided diagnostics. Propose only the necessary file change; use candidate tools to inspect current state as needed. Do not change tests or claim you executed verification."
	}
	if negativeReview {
		prompt = "Ensure generated.txt contains exactly writer followed by one newline. Inspect its current candidate bytes. A different value is a correctness defect even if git-version passed; that check does not validate file contents. Writer should propose only the necessary correction. Reviewer must compare actual candidate bytes with this requirement and report concrete findings for any mismatch. Do not change other files or claim additional tests ran."
	}
	if explore {
		prompt += " Explorer notes are advisory. As writer, independently call both candidate_list and candidate_read before proposing, even if the explorer already supplied the content. This task explicitly requires independent source inspection by both roles."
	}
	run(&s, "plan", prompt)
	run(&s, "approve", s.RunID, s.PlanID, "fixture-operator")
	run(&s, "run", s.RunID)
	if repair {
		run(&s, "verify", s.RunID)
		if s.State != "REPAIRING" || s.Verification == nil || len(s.Verification.Observations) != 1 || s.Verification.Observations[0].Result.Status != "FAIL" {
			t.Fatal("repair fixture did not execute a failure")
		}
	}
	if candidateTools {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			t.Fatal(err)
		}
		expected = "candidate-" + hex.EncodeToString(nonce[:]) + "\n"
		digest := sha256.Sum256([]byte("base\n"))
		beforeHash := hex.EncodeToString(digest[:])
		content := base64.StdEncoding.EncodeToString([]byte(expected))
		path, err := runPath(root, s.RunID)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := control.PrepareFiles(ctx, path, []fileeffects.Change{{Path: "source.txt", BeforeHash: &beforeHash, ContentBase64: &content}})
		if err != nil {
			t.Fatal(err)
		}
		id, err := prepared.Intent.ID()
		if err != nil {
			t.Fatal(err)
		}
		s, err = control.ApplyFiles(ctx, path, prepared, effects.Authorization{IntentID: id, Actor: "fixture-operator"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if negativeReview {
		seedNegativeReview(t, ctx, root, &s)
	}
	if explore {
		before := *s.Candidate
		question := "Use candidate_list and candidate_read to inspect source.txt. Summarize its current content and distinguish it from the base commit. Return the exact candidate_id and paths [source.txt] in the required JSON. Do not modify files."
		var explorerInvocation runtime.Invocation
		run(&explorerInvocation, "prepare-explorer", s.RunID, question)
		var record control.ExplorerRecord
		run(&record, "explore", s.RunID, question)
		run(&s, "inspect", s.RunID)
		if record.Invocation != explorerInvocation || len(s.Explorations) != 1 || s.ExplorerHost == nil || s.ExplorerHost.RuntimeReceipt == nil || *s.Candidate != before {
			t.Fatal("explorer provenance or candidate mismatch")
		}
		assertExplorerReads(t, s)
		// Make the retained synthesis historical so current bytes cannot be copied
		// from it. The writer must retain access to the newly admitted candidate.
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(expected))
		beforeHash := hex.EncodeToString(digest[:])
		expected = "after-exploration-" + hex.EncodeToString(nonce[:]) + "\n"
		content := base64.StdEncoding.EncodeToString([]byte(expected))
		path, err := runPath(root, s.RunID)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := control.PrepareFiles(ctx, path, []fileeffects.Change{{Path: "source.txt", BeforeHash: &beforeHash, ContentBase64: &content}})
		if err != nil {
			t.Fatal(err)
		}
		id, err := prepared.Intent.ID()
		if err != nil {
			t.Fatal(err)
		}
		s, err = control.ApplyFiles(ctx, path, prepared, effects.Authorization{IntentID: id, Actor: "fixture-operator"})
		if err != nil {
			t.Fatal(err)
		}
	}
	var invocation runtime.Invocation
	run(&invocation, "prepare-writer", s.RunID)
	if explore && (!strings.Contains(invocation.Input, `"exploration"`) || !strings.Contains(invocation.Input, `"candidate_current":false`)) {
		t.Fatal("explorer context missing from writer input")
	}
	if negativeReview && (!strings.Contains(invocation.Input, `"decision":"changes_requested"`) || !strings.Contains(invocation.Input, `"review"`)) {
		t.Fatal("negative review absent from repair input")
	}
	if repair && (!strings.Contains(invocation.Input, "generated.txt must contain") || !strings.Contains(invocation.Input, `"status":"FAIL"`)) {
		t.Fatal("repair diagnostics absent from writer input")
	}
	var proposal control.WriterRecord
	run(&proposal, "write", s.RunID)
	if proposal.Invocation != invocation {
		t.Fatal("writer invocation substituted")
	}
	run(&s, "inspect", s.RunID)
	if s.WriterHost == nil || s.WriterHost.RuntimeReceipt == nil || s.WriterProposal == nil {
		t.Fatal("writer execution receipt missing")
	}
	if explore {
		var usage control.RunUsage
		run(&usage, "usage", s.RunID)
		if len(usage.Invocations) != 2 || usage.Invocations[0].Role != "explorer" || usage.Invocations[1].Role != "writer" || !usage.Invocations[0].ReceiptMatched || !usage.Invocations[1].ReceiptMatched {
			t.Fatal("explorer/writer accounting incomplete")
		}
	}
	if candidateTools {
		path := filepath.Join(s.WriterHost.Intent.Launch.Root, "writer.jsonl")
		state, head, err := codexruntime.InspectWithHead(path)
		if err != nil || state.Candidate == nil || state.Candidate.Candidate != *s.Candidate || head != s.WriterHost.RuntimeReceipt.JournalHead {
			t.Fatal("candidate runtime evidence mismatch", err)
		}
		events, err := journal.Read(path)
		if err != nil {
			t.Fatal(err)
		}
		calls := map[string]string{}
		for _, e := range events {
			if e.Kind == "runtime.tool-request" {
				var request codexruntime.ToolRequest
				if err := json.Unmarshal(e.Payload, &request); err != nil {
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
		if !succeeded["candidate_list"] || !succeeded["candidate_read"] || state.PendingTool != nil {
			t.Fatal("successful candidate tool calls not recorded")
		}
	}
	if negativeReview {
		content, err := os.ReadFile(filepath.Join(s.Workspace.Request.Path, "generated.txt"))
		if err != nil || string(content) != "incorrect\n" {
			t.Fatal("writer changed candidate before approval", err)
		}
	} else if _, err := os.Stat(filepath.Join(s.Workspace.Request.Path, "generated.txt")); !os.IsNotExist(err) {
		t.Fatal("writer applied proposal without approval", err)
	}
	intentID, err := proposal.Prepared.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	path, err := runPath(root, s.RunID)
	if err != nil {
		t.Fatal(err)
	}
	s, err = control.ApplyFiles(ctx, path, proposal.Prepared, effects.Authorization{IntentID: intentID, Actor: "fixture-operator"})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(s.Workspace.Request.Path, "generated.txt"))
	if err != nil || string(content) != expected {
		t.Fatal("writer proposal content mismatch", err)
	}
	if _, err := os.Stat(filepath.Join(root, "generated.txt")); !os.IsNotExist(err) {
		t.Fatal("canonical source mutated", err)
	}
	if repair || negativeReview {
		run(&s, "verify", s.RunID)
		want := "READY"
		if review {
			want = "REVIEWING"
		}
		if s.State != want || s.Verification == nil || len(s.Verification.Observations) != 1 || s.Verification.Observations[0].Result.Status != "PASS" {
			t.Fatal("repair did not pass the original configured verification")
		}
	}
	if review {
		before := *s.Candidate
		var invocation runtime.Invocation
		run(&invocation, "prepare-review", s.RunID)
		var record control.ReviewRecord
		run(&record, "review", s.RunID)
		if record.Invocation != invocation {
			t.Fatal("review invocation substituted")
		}
		run(&s, "inspect", s.RunID)
		if s.State != "READY" || s.Review == nil || s.ReviewHost == nil || s.ReviewHost.RuntimeReceipt == nil || *s.Candidate != before {
			t.Fatal("review did not admit unchanged verified candidate")
		}
		if s.ReviewHost.Intent.Launch.Root == s.WriterHost.Intent.Launch.Root {
			t.Fatal("reviewer reused writer host")
		}
		var usage control.RunUsage
		run(&usage, "usage", s.RunID)
		roles := []string{"writer", "reviewer"}
		if negativeReview {
			roles = []string{"reviewer", "writer", "reviewer"}
		}
		if len(usage.Invocations) != len(roles) {
			t.Fatal("run usage omitted hosts")
		}
		for i, role := range roles {
			if usage.Invocations[i].Role != role {
				t.Fatal("run usage reordered hosts")
			}
		}
		for _, entry := range usage.Invocations {
			if !entry.ReceiptMatched || entry.Usage == nil || !entry.Usage.Completed {
				t.Fatal("admitted runtime accounting missing")
			}
		}
	}
}
