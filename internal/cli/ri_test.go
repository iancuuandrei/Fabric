package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/ri"
)

func TestRICommandUsesCommittedRepositoryBinding(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("requires built Rust RI executable")
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	binaryHash := sha256.Sum256(binary)
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	}
	git("init", "-q")
	fixtureText := "persoană persoană persoană"
	if err := os.WriteFile(filepath.Join(root, "names.txt"), []byte(fixtureText), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "names.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "fixture")
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"init"}, root, &output); err != nil {
		t.Fatal(err)
	}
	cfg, err := configuration(root)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := repository.Discover(context.Background(), root, cfg.Repository)
	if err != nil {
		t.Fatal(err)
	}
	source, err := ri.FromRepository(identity)
	if err != nil {
		t.Fatal(err)
	}
	lexical, err := ri.ObserveLexical(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	sourceRoot := filepath.Join(stage, "sources")
	if err := ri.MaterializeLexical(context.Background(), identity, lexical.Manifest, sourceRoot); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(stage, "manifest.jsonl")
	manifestFile, err := os.Create(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := lexical.Manifest.WriteRecords(manifestFile); err != nil {
		_ = manifestFile.Close()
		t.Fatal(err)
	}
	if err := manifestFile.Close(); err != nil {
		t.Fatal(err)
	}
	lexicalClient := ri.Client{Executable: executable, ExecutableHash: hex.EncodeToString(binaryHash[:])}
	indexPath := filepath.Join(stage, "index")
	build, err := lexicalClient.BuildLexical(context.Background(), lexical.Manifest, manifestPath, sourceRoot, indexPath, 1048576, 100)
	if err != nil {
		t.Fatal(err)
	}
	refPath := filepath.Join(stage, "ref.json")
	refBytes, err := canonical.Bytes(ri.LexicalRef{ManifestPath: manifestPath, SourceRoot: sourceRoot, IndexPath: indexPath, Manifest: lexical.Manifest, Build: build})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(refPath, refBytes, 0600); err != nil {
		t.Fatal(err)
	}
	var lexicalOutput bytes.Buffer
	if err := Execute(context.Background(), []string{"ri", "search", executable, hex.EncodeToString(binaryHash[:]), refPath, "--fixed", "--limit", "1", "persoană"}, root, &lexicalOutput); err != nil {
		t.Fatal(err)
	}
	var first ri.LexicalResult
	if err := canonical.Decode(bytes.TrimSpace(lexicalOutput.Bytes()), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Matches) != 1 || !first.Truncated || first.Next == nil {
		t.Fatal("CLI lexical paging failed")
	}
	manifest := map[string]any{"format": 1, "source": source, "producers": []any{map[string]any{"id": "p", "name": "fixture", "version": "1", "artifact_sha256": strings.Repeat("d", 64), "inputs": []any{map[string]any{"name": "fixture", "sha256": strings.Repeat("e", 64)}}}}}
	textHash := sha256.Sum256([]byte(fixtureText))
	manifest["producers"].([]any)[0].(map[string]any)["inputs"] = []any{map[string]any{"name": "source:names.txt", "sha256": hex.EncodeToString(textHash[:])}}
	record, err := canonical.Bytes(map[string]any{"kind": "manifest", "value": manifest})
	if err != nil {
		t.Fatal(err)
	}
	artifact := append(record, '\n')
	appendRecord := func(kind string, value any) {
		t.Helper()
		encoded, err := canonical.Bytes(map[string]any{"kind": kind, "value": value})
		if err != nil {
			t.Fatal(err)
		}
		artifact = append(artifact, encoded...)
		artifact = append(artifact, '\n')
	}
	for _, node := range []string{"a", "b", "c"} {
		appendRecord("node", map[string]any{"id": node, "kind": "MODULE", "path": nil})
	}
	appendRecord("node", map[string]any{"id": "file", "kind": "FILE", "path": "names.txt"})
	appendRecord("node", map[string]any{"id": "symbol", "kind": "SYMBOL", "path": nil})
	appendRecord("edge", ri.Edge{ID: "ab", From: "a", To: "b", Relation: "DEPENDS_ON", Producer: "p", Quality: "DECLARED"})
	appendRecord("edge", ri.Edge{ID: "bc", From: "b", To: "c", Relation: "DEPENDS_ON", Producer: "p", Quality: "DECLARED"})
	for index, occurrenceID := range []string{"definition", "reference1", "reference2"} {
		role := 0
		if index == 0 {
			role = 1
		}
		symbol := "symbol"
		start := index * (len("persoană") + 1)
		appendRecord("occurrence", ri.Occurrence{ID: occurrenceID, Path: "names.txt", SourceSHA256: hex.EncodeToString(textHash[:]), Span: ri.Span{Start: start, End: start + len("persoană")}, Spelling: "persoană", Symbol: &symbol, Roles: &role, Producer: "p", Quality: "DECLARED"})
	}
	id := ri.SnapshotID(artifact)
	path := filepath.Join(t.TempDir(), "snapshot.jsonl")
	if err := os.WriteFile(path, artifact, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"ri", "status", executable, hex.EncodeToString(binaryHash[:]), path, id}
	output.Reset()
	if err := Execute(context.Background(), args, root, &output); err != nil {
		t.Fatal(err)
	}
	var status ri.Status
	if err := canonical.Decode(bytes.TrimSpace(output.Bytes()), &status); err != nil {
		t.Fatal(err)
	}
	if status.Source != source {
		t.Fatal("CLI source binding mismatch")
	}
	output.Reset()
	changedArgs := []string{"ri", "changed", executable, hex.EncodeToString(binaryHash[:]), path, id, identity.Commit, path, id, identity.Commit}
	if err := Execute(context.Background(), changedArgs, root, &output); err != nil {
		t.Fatal(err)
	}
	var unchanged ri.ChangeReport
	if err := canonical.Decode(bytes.TrimSpace(output.Bytes()), &unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged.Changes.SourceIdentityChanged || len(unchanged.Changes.Sources) != 0 {
		t.Fatal("identical snapshots differ", unchanged)
	}
	// The process cwd differs from --root; relative artifacts belong to --root.
	if err := os.WriteFile(filepath.Join(root, "snapshot.jsonl"), artifact, 0600); err != nil {
		t.Fatal(err)
	}
	relativeArgs := []string{"--root", root, "ri", "status", executable, hex.EncodeToString(binaryHash[:]), "snapshot.jsonl", id}
	output.Reset()
	if err := Execute(context.Background(), relativeArgs, t.TempDir(), &output); err != nil {
		t.Fatal("root-relative snapshot:", err)
	}
	runQuery := func(operation string, extra ...string) []byte {
		t.Helper()
		command := append([]string{"ri", operation, executable, hex.EncodeToString(binaryHash[:]), path, id}, extra...)
		output.Reset()
		if err := Execute(context.Background(), command, root, &output); err != nil {
			t.Fatal(err)
		}
		return bytes.TrimSpace(output.Bytes())
	}
	var forward ri.NeighborPage
	var related ri.NeighborPage
	if err := canonical.Decode(runQuery("related", "b", "DEPENDS_ON", "INCOMING", "p", "1"), &related); err != nil {
		t.Fatal(err)
	}
	if len(related.Evidence.Edges) != 1 || related.Evidence.Edges[0].From != "a" {
		t.Fatal("related scope mismatch", related)
	}
	var located ri.OccurrencePage
	if err := canonical.Decode(runQuery("locate", "names.txt", "0", "p", "1"), &located); err != nil {
		t.Fatal(err)
	}
	if len(located.Occurrences) != 1 || located.Occurrences[0].Spelling != "persoană" {
		t.Fatal("locate mismatch", located)
	}
	if err := canonical.Decode(runQuery("locate", "names.txt", "9", "p", "1"), &located); err != nil {
		t.Fatal(err)
	}
	if len(located.Occurrences) != 0 {
		t.Fatal("locate included excluded end", located)
	}
	var definitions ri.OccurrencePage
	if err := canonical.Decode(runQuery("definition", "symbol", "p", "1"), &definitions); err != nil {
		t.Fatal(err)
	}
	if len(definitions.Occurrences) != 1 || definitions.Occurrences[0].Spelling != "persoană" || definitions.NextAfter != nil {
		t.Fatal("definition mismatch", definitions)
	}
	var references ri.OccurrencePage
	if err := canonical.Decode(runQuery("references", "symbol", "p", "1"), &references); err != nil {
		t.Fatal(err)
	}
	if len(references.Occurrences) != 1 || references.NextAfter == nil {
		t.Fatal("reference first page mismatch", references)
	}
	cursor := *references.NextAfter
	if err := canonical.Decode(runQuery("references", "symbol", "p", "1", cursor), &references); err != nil {
		t.Fatal(err)
	}
	if len(references.Occurrences) != 1 || references.Occurrences[0].ID != "reference2" || references.NextAfter != nil {
		t.Fatal("reference continuation mismatch", references)
	}
	if err := canonical.Decode(runQuery("deps", "a", "p", "1"), &forward); err != nil {
		t.Fatal(err)
	}
	if len(forward.Evidence.Edges) != 1 || forward.Evidence.Edges[0].To != "b" {
		t.Fatal("CLI forward dependency mismatch", forward)
	}
	var reverse ri.NeighborPage
	if err := canonical.Decode(runQuery("rdeps", "c", "p", "1"), &reverse); err != nil {
		t.Fatal(err)
	}
	if len(reverse.Evidence.Edges) != 1 || reverse.Evidence.Edges[0].From != "b" {
		t.Fatal("CLI reverse dependency mismatch", reverse)
	}
	var route ri.PathReport
	if err := canonical.Decode(runQuery("path", "a", "c", "DEPENDS_ON", "OUTGOING", "p", "4", "20"), &route); err != nil {
		t.Fatal(err)
	}
	if !route.Evidence.Found || len(route.Evidence.Edges) != 2 {
		t.Fatal("CLI multi-edge path mismatch", route)
	}
	if err := canonical.Decode(runQuery("path", "a", "c", "DEPENDS_ON", "OUTGOING", "p", "1", "20"), &route); err != nil {
		t.Fatal(err)
	}
	if route.Evidence.Found || !route.Evidence.Truncated || route.Evidence.AbsenceProven {
		t.Fatal("CLI depth limit misreported", route)
	}
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "next")
	newText := fixtureText + " updated"
	if err := os.WriteFile(filepath.Join(root, "names.txt"), []byte(newText), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "names.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "change source")
	newIdentity, err := repository.Discover(context.Background(), root, cfg.Repository)
	if err != nil {
		t.Fatal(err)
	}
	newSource, err := ri.FromRepository(newIdentity)
	if err != nil {
		t.Fatal(err)
	}
	newHash := sha256.Sum256([]byte(newText))
	manifest["source"] = newSource
	manifest["producers"].([]any)[0].(map[string]any)["inputs"] = []any{map[string]any{"name": "source:names.txt", "sha256": hex.EncodeToString(newHash[:])}}
	newRecord, err := canonical.Bytes(map[string]any{"kind": "manifest", "value": manifest})
	if err != nil {
		t.Fatal(err)
	}
	newArtifact := append(newRecord, '\n')
	newID := ri.SnapshotID(newArtifact)
	newPath := filepath.Join(t.TempDir(), "after.jsonl")
	if err := os.WriteFile(newPath, newArtifact, 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	changedArgs = []string{"ri", "changed", executable, hex.EncodeToString(binaryHash[:]), path, id, identity.Commit, newPath, newID, newIdentity.Commit}
	if err := Execute(context.Background(), changedArgs, root, &output); err != nil {
		t.Fatal(err)
	}
	var delta ri.ChangeReport
	if err := canonical.Decode(bytes.TrimSpace(output.Bytes()), &delta); err != nil {
		t.Fatal(err)
	}
	if !delta.Changes.SourceIdentityChanged || len(delta.Changes.Sources) != 1 || len(delta.Changes.ChangedProducers) != 0 {
		t.Fatal("changed source classification mismatch", delta)
	}
	change := delta.Changes.Sources[0]
	if change.Path != "names.txt" || change.Before == nil || change.After == nil || *change.Before != hex.EncodeToString(textHash[:]) || *change.After != hex.EncodeToString(newHash[:]) {
		t.Fatal("changed source digest mismatch", change)
	}
	output.Reset()
	pagedArgs := append(append([]string{}, changedArgs...), "1")
	if err := Execute(context.Background(), pagedArgs, root, &output); err != nil {
		t.Fatal(err)
	}
	var changePage ri.ChangePage
	if err := canonical.Decode(bytes.TrimSpace(output.Bytes()), &changePage); err != nil {
		t.Fatal(err)
	}
	if len(changePage.Sources) != 1 || changePage.NextAfter != nil || changePage.Sources[0].Path != "names.txt" {
		t.Fatal("paged change mismatch", changePage)
	}
	// Add another declared source input to require an actual continuation.
	inputs := manifest["producers"].([]any)[0].(map[string]any)["inputs"].([]any)
	manifest["producers"].([]any)[0].(map[string]any)["inputs"] = append(inputs, map[string]any{"name": "source:z.txt", "sha256": hex.EncodeToString(newHash[:])})
	twoRecord, err := canonical.Bytes(map[string]any{"kind": "manifest", "value": manifest})
	if err != nil {
		t.Fatal(err)
	}
	twoArtifact := append(twoRecord, '\n')
	twoID := ri.SnapshotID(twoArtifact)
	twoPath := filepath.Join(t.TempDir(), "two.jsonl")
	if err := os.WriteFile(twoPath, twoArtifact, 0600); err != nil {
		t.Fatal(err)
	}
	pagedArgs = []string{"ri", "changed", executable, hex.EncodeToString(binaryHash[:]), path, id, identity.Commit, twoPath, twoID, newIdentity.Commit, "1"}
	output.Reset()
	if err := Execute(context.Background(), pagedArgs, root, &output); err != nil {
		t.Fatal(err)
	}
	if err := canonical.Decode(bytes.TrimSpace(output.Bytes()), &changePage); err != nil {
		t.Fatal(err)
	}
	if changePage.NextAfter == nil || len(changePage.Sources) != 1 {
		t.Fatal("missing change continuation", changePage)
	}
	nextCursor := *changePage.NextAfter
	output.Reset()
	if err := Execute(context.Background(), append(append([]string{}, pagedArgs...), nextCursor), root, &output); err != nil {
		t.Fatal(err)
	}
	if err := canonical.Decode(bytes.TrimSpace(output.Bytes()), &changePage); err != nil {
		t.Fatal(err)
	}
	if changePage.NextAfter != nil || len(changePage.Sources) != 1 || changePage.Sources[0].Path != "z.txt" || changePage.Sources[0].Before != nil {
		t.Fatal("second change page mismatch", changePage)
	}
	pagedArgs[len(pagedArgs)-1] = "2"
	output.Reset()
	if err := Execute(context.Background(), append(pagedArgs, nextCursor), root, &output); err == nil {
		t.Fatal("changed limit accepted old cursor")
	}
	output.Reset()
	if err := Execute(context.Background(), args, root, &output); err == nil {
		t.Fatal("stale snapshot admitted for new commit")
	}
}
