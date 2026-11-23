package ri

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

func TestSemanticSnapshotQueryValidation(t *testing.T) {
	validReferences := SemanticSnapshotQuery{Vocabulary: "references", Symbol: "symbol", Producer: "scip-go", Limit: 1}
	validImplementations := SemanticSnapshotQuery{Vocabulary: "implementations", Node: "node", Producer: "scip-go", Direction: "OUTGOING", Limit: 128}
	for _, query := range []SemanticSnapshotQuery{validReferences, validImplementations} {
		if err := ValidateSemanticSnapshotQuery(query); err != nil {
			t.Fatalf("valid query rejected: %+v: %v", query, err)
		}
	}
	for _, query := range []SemanticSnapshotQuery{
		{Vocabulary: "references", Symbol: "symbol", Producer: "scip-go", Limit: 1, Direction: "OUTGOING"},
		{Vocabulary: "references", Symbol: "symbol", Producer: "scip-go", Limit: 129},
		{Vocabulary: "implementations", Node: "node", Producer: "scip-go", Direction: "SIDEWAYS", Limit: 1},
		{Vocabulary: "implementations", Node: "node", Symbol: "symbol", Producer: "scip-go", Direction: "INCOMING", Limit: 1},
		{Vocabulary: "calls", Node: "node", Producer: "scip-go", Direction: "OUTGOING", Limit: 1},
	} {
		if err := ValidateSemanticSnapshotQuery(query); err == nil {
			t.Fatalf("invalid query accepted: %+v", query)
		}
	}
}

func TestValidateSemanticSnapshotResultBindsScopeAndCoverage(t *testing.T) {
	ref := SnapshotRef{Path: "C:\\snapshot", ID: "snapshot", Source: Source{RepositoryID: strings.Repeat("a", 64), ObjectFormat: "sha1", Commit: strings.Repeat("b", 40), Tree: strings.Repeat("c", 40)}}
	query := SemanticSnapshotQuery{Vocabulary: "implementations", Node: "node", Producer: "scip-go", Direction: "OUTGOING", Limit: 2}
	result := SemanticSnapshotResult{Version: 1, Vocabulary: query.Vocabulary, SnapshotID: ref.ID, Source: ref.Source, Producer: query.Producer, QueryID: "query", Coverage: "PARTIAL", Occurrences: []Occurrence{}, Edges: []Edge{{ID: "edge", From: "node", To: "target", Relation: "IMPLEMENTS", Producer: "scip-go", Quality: "DECLARED"}}}
	if err := validateSemanticSnapshotResult(result, ref, query); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*SemanticSnapshotResult){
		func(v *SemanticSnapshotResult) { v.SnapshotID = "other" },
		func(v *SemanticSnapshotResult) { v.Source.Commit = strings.Repeat("d", 40) },
		func(v *SemanticSnapshotResult) { v.Producer = "other" },
		func(v *SemanticSnapshotResult) { v.Edges[0].Relation = "CALLS" },
		func(v *SemanticSnapshotResult) { v.Coverage = "COMPLETE"; v.AbsenceProven = true },
		func(v *SemanticSnapshotResult) { v.Occurrences = nil },
	} {
		changed := result
		changed.Edges = append([]Edge{}, result.Edges...)
		mutate(&changed)
		if err := validateSemanticSnapshotResult(changed, ref, query); err == nil {
			t.Fatalf("substituted result accepted: %+v", changed)
		}
	}
}

func TestValidateSemanticSnapshotReferencesRemainPartialWithoutAbsence(t *testing.T) {
	ref := SnapshotRef{Path: "C:\\snapshot", ID: "snapshot", Source: Source{RepositoryID: strings.Repeat("a", 64), ObjectFormat: "sha1", Commit: strings.Repeat("b", 40), Tree: strings.Repeat("c", 40)}}
	query := SemanticSnapshotQuery{Vocabulary: "references", Symbol: "symbol", Producer: "scip-go", Limit: 1}
	result := SemanticSnapshotResult{Version: 1, Vocabulary: query.Vocabulary, SnapshotID: ref.ID, Source: ref.Source, Producer: query.Producer, QueryID: "query", Coverage: "PARTIAL", Occurrences: []Occurrence{}, Edges: []Edge{}}
	if err := validateSemanticSnapshotResult(result, ref, query); err != nil {
		t.Fatal(err)
	}
	result.AbsenceProven = true
	if err := validateSemanticSnapshotResult(result, ref, query); err == nil {
		t.Fatal("reference absence claim accepted")
	}
}

func TestActualSemanticSnapshotQuery(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("set ENGORCH_RI_BINARY to the pinned Rust RI executable")
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	client := Client{Executable: executable, ExecutableHash: hex.EncodeToString(digest[:])}
	ref := publishSemanticSnapshotFixture(t)

	impl := semanticSnapshotFixtureSymbol(t, "impl")
	references, err := client.QuerySemanticSnapshot(context.Background(), ref, SemanticSnapshotQuery{Vocabulary: "references", Symbol: impl, Producer: "p", Limit: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if references.Coverage != "PARTIAL" || references.AbsenceProven || len(references.Occurrences) != 1 || len(references.Edges) != 0 || references.NextAfter == nil || references.Occurrences[0].ID != "occ-a" {
		t.Fatalf("reference page lost narrow semantics: coverage=%q absence=%t occurrences=%d edges=%d next=%t", references.Coverage, references.AbsenceProven, len(references.Occurrences), len(references.Edges), references.NextAfter != nil)
	}
	referenceNext, err := client.QuerySemanticSnapshot(context.Background(), ref, SemanticSnapshotQuery{Vocabulary: "references", Symbol: impl, Producer: "p", Limit: 1}, references.NextAfter)
	if err != nil || len(referenceNext.Occurrences) != 1 || referenceNext.Occurrences[0].ID != "occ-b" || referenceNext.NextAfter != nil {
		t.Fatalf("reference pagination binding failed: err=%v records=%d next=%t", err, len(referenceNext.Occurrences), referenceNext.NextAfter != nil)
	}

	implementations, err := client.QuerySemanticSnapshot(context.Background(), ref, SemanticSnapshotQuery{Vocabulary: "implementations", Node: impl, Producer: "p", Direction: "OUTGOING", Limit: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if implementations.Coverage != "PARTIAL" || implementations.AbsenceProven || len(implementations.Edges) != 1 || implementations.Edges[0].ID != "edge-a" || implementations.NextAfter == nil {
		t.Fatalf("outgoing implementation page lost evidence: coverage=%q absence=%t edges=%d next=%t", implementations.Coverage, implementations.AbsenceProven, len(implementations.Edges), implementations.NextAfter != nil)
	}
	implementationNext, err := client.QuerySemanticSnapshot(context.Background(), ref, SemanticSnapshotQuery{Vocabulary: "implementations", Node: impl, Producer: "p", Direction: "OUTGOING", Limit: 1}, implementations.NextAfter)
	if err != nil || len(implementationNext.Edges) != 1 || implementationNext.Edges[0].ID != "edge-b" || implementationNext.NextAfter != nil {
		t.Fatalf("implementation pagination binding failed: err=%v edges=%d next=%t", err, len(implementationNext.Edges), implementationNext.NextAfter != nil)
	}
	targetA := semanticSnapshotFixtureSymbol(t, "target-a")
	incoming, err := client.QuerySemanticSnapshot(context.Background(), ref, SemanticSnapshotQuery{Vocabulary: "implementations", Node: targetA, Producer: "p", Direction: "INCOMING", Limit: 1}, nil)
	if err != nil || incoming.Coverage != "UNKNOWN" || incoming.AbsenceProven || len(incoming.Edges) != 1 || incoming.Edges[0].From != impl || incoming.Edges[0].To != targetA {
		t.Fatalf("incoming implementation changed directional coverage: err=%v coverage=%q absence=%t edges=%d", err, incoming.Coverage, incoming.AbsenceProven, len(incoming.Edges))
	}
}

func publishSemanticSnapshotFixture(t *testing.T) SnapshotRef {
	t.Helper()
	sourceText := []byte("ab")
	sourceDigest := sha256.Sum256(sourceText)
	source := Source{RepositoryID: strings.Repeat("a", 64), ObjectFormat: "sha1", Commit: strings.Repeat("b", 40), Tree: strings.Repeat("c", 40)}
	manifest := map[string]any{"format": 1, "source": source, "producers": []any{map[string]any{"id": "p", "name": "fixture", "version": "1", "artifact_sha256": strings.Repeat("d", 64), "inputs": []any{map[string]any{"name": "source:a.go", "sha256": hex.EncodeToString(sourceDigest[:])}}}}}
	impl := semanticSnapshotFixtureSymbol(t, "impl")
	targetA := semanticSnapshotFixtureSymbol(t, "target-a")
	targetB := semanticSnapshotFixtureSymbol(t, "target-b")
	nodes := []map[string]any{
		{"id": "file", "kind": "FILE", "path": "a.go"},
		{"id": impl, "kind": "SYMBOL", "path": "a.go"},
		{"id": targetA, "kind": "SYMBOL", "path": "a.go"},
		{"id": targetB, "kind": "SYMBOL", "path": "a.go"},
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i]["id"].(string) < nodes[j]["id"].(string) })
	records := []map[string]any{
		{"kind": "manifest", "value": manifest},
	}
	for _, node := range nodes {
		records = append(records, map[string]any{"kind": "node", "value": node})
	}
	records = append(records,
		map[string]any{"kind": "edge", "value": map[string]any{"id": "edge-a", "from": impl, "to": targetA, "relation": "IMPLEMENTS", "producer": "p", "quality": "DECLARED"}},
		map[string]any{"kind": "edge", "value": map[string]any{"id": "edge-b", "from": impl, "to": targetB, "relation": "IMPLEMENTS", "producer": "p", "quality": "DECLARED"}},
		map[string]any{"kind": "coverage", "value": map[string]any{"node": impl, "relation": "IMPLEMENTS", "direction": "OUTGOING", "producer": "p", "completeness": "PARTIAL"}},
		map[string]any{"kind": "occurrence", "value": map[string]any{"id": "occ-a", "path": "a.go", "source_sha256": hex.EncodeToString(sourceDigest[:]), "span": map[string]any{"start": 0, "end": 1}, "spelling": "a", "symbol": impl, "roles": 0, "producer": "p", "quality": "DECLARED"}},
		map[string]any{"kind": "occurrence", "value": map[string]any{"id": "occ-b", "path": "a.go", "source_sha256": hex.EncodeToString(sourceDigest[:]), "span": map[string]any{"start": 1, "end": 2}, "spelling": "b", "symbol": impl, "roles": 0, "producer": "p", "quality": "DECLARED"}},
	)
	var artifact []byte
	for _, record := range records {
		encoded, err := canonical.Bytes(record)
		if err != nil {
			t.Fatal(err)
		}
		artifact = append(artifact, encoded...)
		artifact = append(artifact, '\n')
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("harness.ri.snapshot.v1\n"))
	_, _ = hash.Write(artifact)
	id := hex.EncodeToString(hash.Sum(nil))
	path, err := (Store{Directory: t.TempDir()}).Publish(id, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) {
		t.Fatal("fixture snapshot path is not absolute")
	}
	return SnapshotRef{Path: path, ID: id, Source: source}
}

func semanticSnapshotFixtureSymbol(t *testing.T, symbol string) string {
	t.Helper()
	id, err := canonical.Hash("harness.ri.scip-symbol.v1", map[string]any{"producer": "p", "symbol": symbol, "document": nil})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
