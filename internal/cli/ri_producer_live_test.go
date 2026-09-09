package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"harness.local/engorch/internal/codexruntime"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/ri"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestActualScipGoCLI(t *testing.T) {
	producer := os.Getenv("ENGORCH_SCIP_GO_BINARY")
	rust := os.Getenv("ENGORCH_RI_BINARY")
	if producer == "" || rust == "" {
		t.Skip("requires scip-go and Rust RI")
	}
	root := t.TempDir()
	for name, content := range map[string]string{"go.mod": "module example.test/clifixture\n\ngo 1.25\n", "library.go": "package clifixture\n\nfunc Greeting() string { return \"salut\" }\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "go.mod", "library.go"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	run := func(args ...string) []byte {
		t.Helper()
		var out bytes.Buffer
		if err := Execute(context.Background(), args, root, &out); err != nil {
			t.Fatal(args, err)
		}
		return out.Bytes()
	}
	write := func(name string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	decode := func(data []byte, value any) {
		t.Helper()
		if err := json.Unmarshal(data, value); err != nil {
			t.Fatal(err)
		}
	}
	run("init")
	var s control.Snapshot
	decode(run("plan", "Index module"), &s)
	run("approve", s.RunID, s.PlanID, "operator")
	decode(run("run", s.RunID), &s)
	index := filepath.Join(root, "index.scip")
	write("check.json", config.Check{Name: "scip-go", Argv: []string{producer, "index", "--module-version=fixture-v1", "--output", index}, TimeoutSeconds: 60})
	var preview producerPreview
	decode(run("ri", "prepare-producer", s.RunID, "check.json", "index.scip"), &preview)
	write("producer.json", preview)
	decode(run("ri", "produce", s.RunID, "producer.json", preview.IntentID, "operator"), &s)
	if s.RIProducer == nil || s.RIProducer.Outcome != "CONFIRMED" {
		t.Fatal("CLI producer not confirmed")
	}
	binary, err := os.ReadFile(rust)
	if err != nil {
		t.Fatal(err)
	}
	source, err := ri.FromRepository(s.Creation.Repository)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := repository.DigestSource(context.Background(), s.Creation.Repository, "library.go")
	if err != nil {
		t.Fatal(err)
	}
	uri := url.URL{Scheme: "file", Path: s.Workspace.Request.Path}
	policy := fmt.Sprintf("%x", sha256.Sum256([]byte("engorch.scip-go.0.2.7.positions.v1:utf8-byte-columns")))
	plan := ri.ImportPlan{Version: 1, Executable: rust, ExecutableSHA256: fmt.Sprintf("%x", sha256.Sum256(binary)), Repository: s.Creation.Repository, OutputPath: filepath.Join(root, "snapshot.staging"), Request: ri.ImportRequest{Source: source, Producer: "scip-go", ProjectRoot: uri.String(), Policy: "scip_go027", Sources: map[string]string{"library.go": filepath.Join(s.Workspace.Request.Path, "library.go")}, Manifest: ri.Manifest{Format: 1, Source: source, Producers: []ri.Producer{{ID: "scip-go", Name: "scip-go", Version: "0.2.7", Inputs: []ri.Input{{Name: "source:library.go", SHA256: digest.SHA256}, {Name: "scip:position-policy", SHA256: policy}}}}}}}
	write("proposal.json", plan)
	decode(run("ri", "bind-import", s.RunID, "proposal.json"), &plan)
	if plan.ProducerIntentID == "" {
		t.Fatal("producer provenance missing")
	}
	write("bound.json", plan)
	var approval struct {
		IntentID string `json:"intent_id"`
	}
	decode(run("ri", "prepare-import", s.RunID, "bound.json"), &approval)
	decode(run("ri", "import", s.RunID, "bound.json", approval.IntentID, "operator"), &s)
	if s.RIImport == nil || s.RIImport.Outcome != "CONFIRMED" {
		t.Fatal("CLI real index import not confirmed")
	}
	if err := os.Mkdir(filepath.Join(root, "store"), 0700); err != nil {
		t.Fatal(err)
	}
	decode(run("ri", "prepare-publish", s.RunID, "store"), &approval)
	decode(run("ri", "publish", s.RunID, "store", approval.IntentID, "operator"), &s)
	if s.RIPublish == nil || s.RIPublish.Outcome != "CONFIRMED" {
		t.Fatal("real index publication failed")
	}
	artifact := s.RIPublish.Observation.Artifact
	var selected struct {
		Snapshot         ri.SnapshotRef `json:"snapshot"`
		Executable       string         `json:"executable"`
		ExecutableSHA256 string         `json:"executable_sha256"`
	}
	decode(run("ri", "runtime-binding", s.RunID), &selected)
	if selected.Snapshot != *artifact || selected.Executable != rust || selected.ExecutableSHA256 != plan.ExecutableSHA256 {
		t.Fatal("CLI runtime binding differs from admitted publication")
	}
	contents, err := os.ReadFile(filepath.Join(root, "library.go"))
	if err != nil {
		t.Fatal(err)
	}
	var located ri.OccurrencePage
	decode(run("ri", "locate", rust, plan.ExecutableSHA256, artifact.Path, artifact.ID, "library.go", strconv.Itoa(strings.Index(string(contents), "Greeting")), "scip-go", "16"), &located)
	if len(located.Occurrences) != 1 || located.Occurrences[0].Symbol == nil {
		t.Fatal("published real symbol not located")
	}
	var definitions ri.OccurrencePage
	decode(run("ri", "definition", rust, plan.ExecutableSHA256, artifact.Path, artifact.ID, *located.Occurrences[0].Symbol, "scip-go", "16"), &definitions)
	if len(definitions.Occurrences) != 1 || definitions.Occurrences[0].Spelling != "Greeting" || definitions.Occurrences[0].SourceSHA256 != digest.SHA256 {
		t.Fatal("published definition provenance mismatch")
	}
	exercisePublishedBroker(t, s.Creation.Repository, codexruntime.RIBinding{Snapshot: selected.Snapshot, Executable: selected.Executable, ExecutableSHA256: selected.ExecutableSHA256}, strings.Index(string(contents), "Greeting"), digest.SHA256)
}
