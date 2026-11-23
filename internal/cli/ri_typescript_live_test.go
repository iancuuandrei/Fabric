package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestActualScipTypeScriptCLI exercises a real scip-typescript 0.4.0 index
// through the admitted controller lifecycle. It requires an explicit Node
// binary, the published entry script and a built Rust RI binary.
func TestActualScipTypeScriptCLI(t *testing.T) {
	node := os.Getenv("ENGORCH_SCIP_TYPESCRIPT_NODE")
	entry := os.Getenv("ENGORCH_SCIP_TYPESCRIPT_ENTRY")
	rust := os.Getenv("ENGORCH_RI_BINARY")
	if node == "" || entry == "" || rust == "" {
		t.Skip("requires scip-typescript node, entry and Rust RI")
	}
	// Normalize executable paths so forward-slash Windows values satisfy the
	// normalized absolute-path checks in import planning.
	node = filepath.Clean(node)
	entry = filepath.Clean(entry)
	rust = filepath.Clean(rust)
	root := t.TempDir()
	library := "/* \U0001F600 */ export function Greeting() { return \"hi\"; }\nexport const abcdef = Greeting();\n"
	fixtures := map[string]string{
		"package.json":  "{\"name\":\"tsfixture\",\"version\":\"1.0.0\",\"private\":true}\n",
		"tsconfig.json": "{\"compilerOptions\":{\"target\":\"ES2020\",\"module\":\"commonjs\",\"strict\":true,\"skipLibCheck\":true,\"noLib\":true},\"include\":[\"library.ts\"]}\n",
		"library.ts":    library,
	}
	for name, content := range fixtures {
		if strings.Contains(content, "\r") {
			t.Fatal("fixture must use LF line endings")
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	commitLiveFixture(t, root, "package.json", "tsconfig.json", "library.ts")
	h := newCLILiveHarness(t, root)
	h.run("init")
	var s control.Snapshot
	h.decode(h.run("plan", "Index TypeScript module"), &s)
	h.run("approve", s.RunID, s.PlanID, "operator")
	h.decode(h.run("run", s.RunID), &s)
	index := filepath.Join(root, "index.scip")
	h.write("check.json", config.Check{Name: "scip-typescript", Argv: []string{node, entry, "index", "--output", index, "--no-progress-bar"}, TimeoutSeconds: 120})
	binary, source := confirmLiveProducer(t, h, &s, rust)
	digestSource := func(name string) repository.SourceDigest {
		t.Helper()
		digest, err := repository.DigestSource(context.Background(), s.Creation.Repository, name)
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}
	digest := digestSource("library.ts")
	packageDigest := digestSource("package.json")
	tsconfigDigest := digestSource("tsconfig.json")
	entryBytes, err := os.ReadFile(entry)
	if err != nil {
		t.Fatal(err)
	}
	// The admitted project-root URI must use Node's url.pathToFileURL
	// formatting, including Windows drive-letter handling.
	pathURL, err := exec.Command(node, "-e", "process.stdout.write(require('url').pathToFileURL(process.argv[1]).href)", s.Workspace.Request.Path).Output()
	if err != nil {
		t.Fatal(err)
	}
	projectRoot := string(pathURL)
	if !strings.HasPrefix(projectRoot, "file:") {
		t.Fatal("Node file URL missing", projectRoot)
	}
	policy := fmt.Sprintf("%x", sha256.Sum256([]byte("engorch.scip-typescript.0.4.0.positions.v2:utf16-code-units+omit-invalid-synthetic-file-enclosing")))
	plan := ri.ImportPlan{Version: 1, Executable: rust, ExecutableSHA256: fmt.Sprintf("%x", sha256.Sum256(binary)), Repository: s.Creation.Repository, OutputPath: filepath.Join(root, "snapshot.staging"), Request: ri.ImportRequest{Source: source, Producer: "scip-typescript", ProjectRoot: projectRoot, Policy: "scip_typescript040", Sources: map[string]string{"library.ts": filepath.Join(s.Workspace.Request.Path, "library.ts")}, Manifest: ri.Manifest{Format: 1, Source: source, Producers: []ri.Producer{{ID: "scip-typescript", Name: "scip-typescript", Version: "0.4.0", Inputs: []ri.Input{{Name: "source:library.ts", SHA256: digest.SHA256}, {Name: "config:package.json", SHA256: packageDigest.SHA256}, {Name: "config:tsconfig.json", SHA256: tsconfigDigest.SHA256}, {Name: "entry:scip-typescript", SHA256: fmt.Sprintf("%x", sha256.Sum256(entryBytes))}, {Name: "scip:position-policy", SHA256: policy}}}}}}}
	artifact, _ := confirmLiveBoundImportPublish(t, h, &s, &plan, rust)
	contents, err := os.ReadFile(filepath.Join(s.Workspace.Request.Path, "library.ts"))
	if err != nil {
		t.Fatal(err)
	}
	offset := strings.Index(string(contents), "Greeting")
	if offset < 0 {
		t.Fatal("fixture spelling absent")
	}
	var located ri.OccurrencePage
	h.decode(h.run("ri", "locate", rust, plan.ExecutableSHA256, artifact.Path, artifact.ID, "library.ts", strconv.Itoa(offset), "scip-typescript", "16"), &located)
	if len(located.Occurrences) != 1 || located.Occurrences[0].Symbol == nil {
		t.Fatal("published real symbol not located")
	}
	if located.AbsenceProven {
		t.Fatal("locate claimed absence")
	}
	var definitions ri.OccurrencePage
	h.decode(h.run("ri", "definition", rust, plan.ExecutableSHA256, artifact.Path, artifact.ID, *located.Occurrences[0].Symbol, "scip-typescript", "16"), &definitions)
	if len(definitions.Occurrences) != 1 || definitions.Occurrences[0].Spelling != "Greeting" || definitions.Occurrences[0].SourceSHA256 != digest.SHA256 || definitions.AbsenceProven {
		t.Fatal("published definition provenance mismatch")
	}
	var references ri.OccurrencePage
	h.decode(h.run("ri", "references", rust, plan.ExecutableSHA256, artifact.Path, artifact.ID, *located.Occurrences[0].Symbol, "scip-typescript", "16"), &references)
	if len(references.Occurrences) < 1 || references.AbsenceProven {
		t.Fatal("published references missing or absence claimed")
	}
	requireSource := func(records []ri.Occurrence, what string) {
		t.Helper()
		for _, record := range records {
			if record.SourceSHA256 != digest.SHA256 {
				t.Fatal(what, "source hash mismatch")
			}
		}
	}
	requireSource(references.Occurrences, "reference")
	h.write("semantic-query.json", map[string]any{"vocabulary": "references", "symbol": *located.Occurrences[0].Symbol, "producer": "scip-typescript", "limit": 16})
	var semantic ri.SemanticSnapshotResult
	h.decode(h.run("ri", "semantic", rust, plan.ExecutableSHA256, artifact.Path, artifact.ID, "semantic-query.json"), &semantic)
	if semantic.Coverage != "PARTIAL" || semantic.AbsenceProven || len(semantic.Occurrences) < 1 {
		t.Fatal("semantic reference coverage mismatch")
	}
	requireSource(semantic.Occurrences, "semantic")
}
