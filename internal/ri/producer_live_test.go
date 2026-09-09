package ri

import (
	"context"
	"crypto/sha256"
	"fmt"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/repository"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestActualScipGoProducer(t *testing.T) {
	executable := os.Getenv("ENGORCH_SCIP_GO_BINARY")
	if executable == "" {
		t.Skip("requires installed scip-go producer")
	}
	root := t.TempDir()
	for name, content := range map[string]string{"go.mod": "module example.test/producerfixture\n\ngo 1.25\n", "library.go": "package producerfixture\n\nfunc Greeting() string { return \"salut\" }\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "go.mod", "library.go"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	identity, err := repository.Discover(context.Background(), root, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "index.scip")
	plan, err := PrepareProducer(identity, root, config.Check{Name: "scip-go", Argv: []string{executable, "index", "--module-version=fixture-v1", "--output", output}, TimeoutSeconds: 60}, output)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ExecuteProducer(context.Background(), plan)
	if err != nil {
		t.Fatal(err, result.Process.Stderr.Excerpt, result.Process.Stdout.Excerpt)
	}
	if result.Artifact == nil || result.Artifact.Bytes < 1 || result.Process.Status != "PASS" {
		t.Fatal("producer artifact not observed")
	}
	if err := ValidateProducerResult(plan, result); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ProducerResult){
		func(r *ProducerResult) { r.PlanID = "foreign" },
		func(r *ProducerResult) { r.Process.InvocationID = "foreign" },
		func(r *ProducerResult) { r.Artifact.Path = filepath.Join(root, "foreign.scip") },
		func(r *ProducerResult) { r.Artifact.Bytes = 0 },
		func(r *ProducerResult) { r.Artifact.SHA256 = "invalid" },
	} {
		altered := result
		artifact := *result.Artifact
		altered.Artifact = &artifact
		mutate(&altered)
		if ValidateProducerResult(plan, altered) == nil {
			t.Fatal("substituted producer receipt accepted")
		}
	}
	incomplete := result
	incomplete.Artifact = nil
	if err := ValidateProducerResult(plan, incomplete); err != nil {
		t.Fatal("incomplete observation rejected", err)
	}
	if _, err := ExecuteProducer(context.Background(), plan); err == nil {
		t.Fatal("existing producer output overwritten")
	}
	t.Run("RustSnapshotAdmission", func(t *testing.T) {
		riBinary := os.Getenv("ENGORCH_RI_BINARY")
		if riBinary == "" {
			t.Skip("requires built Rust RI for producer/import interoperability")
		}
		binary, err := os.ReadFile(riBinary)
		if err != nil {
			t.Fatal(err)
		}
		client := Client{Executable: riBinary, ExecutableHash: fmt.Sprintf("%x", sha256.Sum256(binary))}
		source, err := FromRepository(identity)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := repository.DigestSource(context.Background(), identity, "library.go")
		if err != nil {
			t.Fatal(err)
		}
		projectRoot := url.URL{Scheme: "file", Path: root}
		policy := "engorch.scip-go.0.2.7.positions.v1:utf8-byte-columns"
		request := ImportRequest{IndexPath: output, Source: source, Producer: "scip-go", ProjectRoot: projectRoot.String(), Sources: map[string]string{"library.go": filepath.Join(root, "library.go")}, Policy: "scip_go027", Manifest: Manifest{Format: 1, Source: source, Producers: []Producer{{ID: "scip-go", Name: "scip-go", Version: "0.2.7", ArtifactSHA256: plan.Invocation.Executable.Hash, Inputs: []Input{{Name: "scip:index", SHA256: result.Artifact.SHA256}, {Name: "scip:position-policy", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(policy)))}, {Name: "source:library.go", SHA256: digest.SHA256}}}}}}
		staging := filepath.Join(t.TempDir(), "snapshot.staging")
		importPlan := ImportPlan{Version: 1, Executable: riBinary, ExecutableSHA256: client.ExecutableHash, Repository: identity, Request: request, OutputPath: staging}
		if _, err := VerifyCommittedSources(context.Background(), importPlan); err != nil {
			t.Fatal(err)
		}
		receipt, err := client.Import(context.Background(), request, identity, staging)
		if err != nil {
			t.Fatal(err)
		}
		status, err := client.Inspect(context.Background(), SnapshotRef{Path: staging, ID: receipt.SnapshotID, Source: source})
		if err != nil || status.Occurrences < 1 || status.Nodes < 2 {
			t.Fatal("real producer index lost semantic observations", status, err)
		}
		contents, err := os.ReadFile(filepath.Join(root, "library.go"))
		if err != nil {
			t.Fatal(err)
		}
		ref := SnapshotRef{Path: staging, ID: receipt.SnapshotID, Source: source}
		located, err := client.Locate(context.Background(), ref, LocateQuery{Path: "library.go", Offset: strings.Index(string(contents), "Greeting"), Producer: "scip-go", Limit: 16}, nil)
		if err != nil || len(located.Occurrences) != 1 || located.Occurrences[0].Symbol == nil {
			t.Fatal("real source locate failed", err)
		}
		definitions, err := client.Occurrences(context.Background(), ref, OccurrenceQuery{Symbol: *located.Occurrences[0].Symbol, Producer: "scip-go", Definitions: true, Limit: 16}, nil)
		if err != nil || len(definitions.Occurrences) != 1 || definitions.Occurrences[0].Spelling != "Greeting" {
			t.Fatal("real definition query failed", err)
		}
		t.Logf("admitted %d nodes and %d occurrences; Greeting definition verified", status.Nodes, status.Occurrences)
	})
}
