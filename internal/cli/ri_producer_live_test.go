package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func commitLiveFixture(t *testing.T, root string, files ...string) {
	t.Helper()
	steps := [][]string{{"init", "-q"}, append([]string{"add"}, files...), {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture"}}
	for _, args := range steps {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
}

type cliLiveHarness struct {
	t    *testing.T
	root string
}

func newCLILiveHarness(t *testing.T, root string) cliLiveHarness {
	t.Helper()
	return cliLiveHarness{t: t, root: root}
}

func (h cliLiveHarness) run(args ...string) []byte {
	h.t.Helper()
	var out bytes.Buffer
	if err := Execute(context.Background(), args, h.root, &out); err != nil {
		h.t.Fatal(args, err)
	}
	return out.Bytes()
}

func (h cliLiveHarness) write(name string, value any) {
	h.t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root, name), data, 0600); err != nil {
		h.t.Fatal(err)
	}
}

func (h cliLiveHarness) decode(data []byte, value any) {
	h.t.Helper()
	if err := json.Unmarshal(data, value); err != nil {
		h.t.Fatal(err)
	}
}

func confirmLiveProducer(t *testing.T, h cliLiveHarness, snapshot *control.Snapshot, rust string) ([]byte, ri.Source) {
	t.Helper()
	var preview producerPreview
	h.decode(h.run("ri", "prepare-producer", snapshot.RunID, "check.json", "index.scip"), &preview)
	h.write("producer.json", preview)
	h.decode(h.run("ri", "produce", snapshot.RunID, "producer.json", preview.IntentID, "operator"), snapshot)
	if snapshot.RIProducer == nil || snapshot.RIProducer.Outcome != "CONFIRMED" {
		t.Fatal("CLI producer not confirmed")
	}
	binary, err := os.ReadFile(rust)
	if err != nil {
		t.Fatal(err)
	}
	source, err := ri.FromRepository(snapshot.Creation.Repository)
	if err != nil {
		t.Fatal(err)
	}
	return binary, source
}

type liveRuntimeBinding struct {
	Snapshot         ri.SnapshotRef `json:"snapshot"`
	Executable       string         `json:"executable"`
	ExecutableSHA256 string         `json:"executable_sha256"`
}

func confirmLiveBoundImportPublish(t *testing.T, h cliLiveHarness, snapshot *control.Snapshot, plan *ri.ImportPlan, rust string) (*ri.SnapshotRef, liveRuntimeBinding) {
	t.Helper()
	h.write("proposal.json", *plan)
	h.decode(h.run("ri", "bind-import", snapshot.RunID, "proposal.json"), plan)
	if plan.ProducerIntentID == "" {
		t.Fatal("producer provenance missing")
	}
	h.write("bound.json", *plan)
	var approval struct {
		IntentID string `json:"intent_id"`
	}
	h.decode(h.run("ri", "prepare-import", snapshot.RunID, "bound.json"), &approval)
	h.decode(h.run("ri", "import", snapshot.RunID, "bound.json", approval.IntentID, "operator"), snapshot)
	if snapshot.RIImport == nil || snapshot.RIImport.Outcome != "CONFIRMED" {
		t.Fatal("CLI real index import not confirmed")
	}
	if err := os.Mkdir(filepath.Join(h.root, "store"), 0700); err != nil {
		t.Fatal(err)
	}
	h.decode(h.run("ri", "prepare-publish", snapshot.RunID, "store"), &approval)
	h.decode(h.run("ri", "publish", snapshot.RunID, "store", approval.IntentID, "operator"), snapshot)
	if snapshot.RIPublish == nil || snapshot.RIPublish.Outcome != "CONFIRMED" {
		t.Fatal("real index publication failed")
	}
	artifact := snapshot.RIPublish.Observation.Artifact
	var selected liveRuntimeBinding
	h.decode(h.run("ri", "runtime-binding", snapshot.RunID), &selected)
	if selected.Snapshot != *artifact || selected.Executable != rust || selected.ExecutableSHA256 != plan.ExecutableSHA256 {
		t.Fatal("CLI runtime binding differs from admitted publication")
	}
	return artifact, selected
}

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
	commitLiveFixture(t, root, "go.mod", "library.go")
	h := newCLILiveHarness(t, root)
	h.run("init")
	var s control.Snapshot
	h.decode(h.run("plan", "Index module"), &s)
	h.run("approve", s.RunID, s.PlanID, "operator")
	h.decode(h.run("run", s.RunID), &s)
	index := filepath.Join(root, "index.scip")
	h.write("check.json", config.Check{Name: "scip-go", Argv: []string{producer, "index", "--module-version=fixture-v1", "--output", index}, TimeoutSeconds: 60})
	binary, source := confirmLiveProducer(t, h, &s, rust)
	digest, err := repository.DigestSource(context.Background(), s.Creation.Repository, "library.go")
	if err != nil {
		t.Fatal(err)
	}
	uri := url.URL{Scheme: "file", Path: s.Workspace.Request.Path}
	policy := fmt.Sprintf("%x", sha256.Sum256([]byte("engorch.scip-go.0.2.7.positions.v1:utf8-byte-columns")))
	plan := ri.ImportPlan{Version: 1, Executable: rust, ExecutableSHA256: fmt.Sprintf("%x", sha256.Sum256(binary)), Repository: s.Creation.Repository, OutputPath: filepath.Join(root, "snapshot.staging"), Request: ri.ImportRequest{Source: source, Producer: "scip-go", ProjectRoot: uri.String(), Policy: "scip_go027", Sources: map[string]string{"library.go": filepath.Join(s.Workspace.Request.Path, "library.go")}, Manifest: ri.Manifest{Format: 1, Source: source, Producers: []ri.Producer{{ID: "scip-go", Name: "scip-go", Version: "0.2.7", Inputs: []ri.Input{{Name: "source:library.go", SHA256: digest.SHA256}, {Name: "scip:position-policy", SHA256: policy}}}}}}}
	artifact, selected := confirmLiveBoundImportPublish(t, h, &s, &plan, rust)
	contents, err := os.ReadFile(filepath.Join(root, "library.go"))
	if err != nil {
		t.Fatal(err)
	}
	var located ri.OccurrencePage
	h.decode(h.run("ri", "locate", rust, plan.ExecutableSHA256, artifact.Path, artifact.ID, "library.go", strconv.Itoa(strings.Index(string(contents), "Greeting")), "scip-go", "16"), &located)
	if len(located.Occurrences) != 1 || located.Occurrences[0].Symbol == nil {
		t.Fatal("published real symbol not located")
	}
	var definitions ri.OccurrencePage
	h.decode(h.run("ri", "definition", rust, plan.ExecutableSHA256, artifact.Path, artifact.ID, *located.Occurrences[0].Symbol, "scip-go", "16"), &definitions)
	if len(definitions.Occurrences) != 1 || definitions.Occurrences[0].Spelling != "Greeting" || definitions.Occurrences[0].SourceSHA256 != digest.SHA256 {
		t.Fatal("published definition provenance mismatch")
	}
	exercisePublishedBroker(t, s.Creation.Repository, codexruntime.RIBinding{Snapshot: selected.Snapshot, Executable: selected.Executable, ExecutableSHA256: selected.ExecutableSHA256}, strings.Index(string(contents), "Greeting"), digest.SHA256)
}
