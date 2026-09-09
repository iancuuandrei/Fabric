package ri

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"harness.local/engorch/internal/canonical"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedExecutableAndCancellation(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	client := Client{executable, strings.Repeat("0", 64)}
	if _, err := client.Call(context.Background(), map[string]any{}); err == nil {
		t.Fatal("substituted executable admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Call(ctx, map[string]any{}); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestActualRustProcessFromGo(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("set ENGORCH_RI_BINARY to built Rust executable")
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(bytes)
	client := Client{executable, hex.EncodeToString(digest[:])}
	source := Source{strings.Repeat("a", 64), "sha1", strings.Repeat("b", 40), strings.Repeat("c", 40)}
	manifest := map[string]any{"format": 1, "source": source, "producers": []any{map[string]any{"id": "p", "name": "fixture", "version": "1", "artifact_sha256": strings.Repeat("d", 64), "inputs": []any{map[string]any{"name": "source", "sha256": strings.Repeat("e", 64)}}}}}
	record, err := canonical.Bytes(map[string]any{"kind": "manifest", "value": manifest})
	if err != nil {
		t.Fatal(err)
	}
	artifact := append(record, '\n')
	node, err := canonical.Bytes(map[string]any{"kind": "node", "value": map[string]any{"id": "a", "kind": "FILE", "path": "a.go"}})
	if err != nil {
		t.Fatal(err)
	}
	artifact = append(artifact, node...)
	artifact = append(artifact, '\n')
	for _, edgeID := range []string{"edge1", "edge2"} {
		edge, err := canonical.Bytes(map[string]any{"kind": "edge", "value": map[string]any{"id": edgeID, "from": "a", "to": "a", "relation": "DEPENDS_ON", "producer": "p", "quality": "DECLARED"}})
		if err != nil {
			t.Fatal(err)
		}
		artifact = append(artifact, edge...)
		artifact = append(artifact, '\n')
	}
	hash := sha256.New()
	hash.Write([]byte("harness.ri.snapshot.v1\n"))
	hash.Write(artifact)
	id := hex.EncodeToString(hash.Sum(nil))
	store := Store{t.TempDir()}
	path, err := store.Publish(id, artifact)
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"operation": "status", "path": path, "snapshot_id": id, "source": source}
	response, err := client.Call(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		SnapshotID      string   `json:"snapshot_id"`
		Source          Source   `json:"source"`
		Nodes           int      `json:"nodes"`
		Edges           int      `json:"edges"`
		CoverageRecords int      `json:"coverage_records"`
		Occurrences     int      `json:"occurrences"`
		Producers       []string `json:"producers"`
	}
	if err := canonical.Decode(response, &status); err != nil {
		t.Fatal(err)
	}
	if status.SnapshotID != id || status.Source != source || status.Nodes != 1 {
		t.Fatal("RI response source mismatch")
	}
	if len(status.Producers) != 1 || status.Producers[0] != "p" {
		t.Fatal("RI status omitted registered producer")
	}
	ref := SnapshotRef{Path: path, ID: id, Source: source}
	pathResult, err := client.Path(context.Background(), ref, PathQuery{From: "a", To: "a", Relation: "DEPENDS_ON", Direction: "OUTGOING", Producer: "p", MaxDepth: 4, MaxEdges: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !pathResult.Evidence.Found || len(pathResult.Evidence.Edges) != 0 {
		t.Fatal("invalid self path", pathResult)
	}
	page, err := client.Neighbors(context.Background(), ref, NeighborQuery{Node: "a", Relation: "CALLS", Direction: "OUTGOING", Producer: "p", Limit: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Evidence.Edges) != 0 || page.Evidence.AbsenceProven || page.NextAfter != nil {
		t.Fatal("invalid empty incomplete neighbor page", page)
	}
	query := NeighborQuery{Node: "a", Relation: "DEPENDS_ON", Direction: "OUTGOING", Producer: "p", Limit: 1}
	first, err := client.Neighbors(context.Background(), ref, query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Evidence.Edges) != 1 || first.Evidence.Edges[0].ID != "edge1" || first.NextAfter == nil {
		t.Fatal("missing first dependency page", first)
	}
	second, err := client.Neighbors(context.Background(), ref, query, first.NextAfter)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Evidence.Edges) != 1 || second.Evidence.Edges[0].ID != "edge2" || second.NextAfter != nil {
		t.Fatal("invalid final dependency page", second)
	}
	verified, err := client.Inspect(context.Background(), ref)
	if err != nil || verified.Nodes != 1 {
		t.Fatalf("typed status: %+v %v", verified, err)
	}
	coverage, err := client.Coverage(context.Background(), ref, CoverageScope{Node: "a", Relation: "REFERENCES", Direction: "OUTGOING"})
	if err != nil {
		t.Fatal(err)
	}
	if len(coverage.Declarations) != 1 || coverage.Declarations[0].Producer != "p" || coverage.Declarations[0].Completeness != nil {
		t.Fatal("coverage declaration changed", coverage)
	}
	if err := os.WriteFile(path, []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), request); err == nil {
		t.Fatal("Rust admitted changed snapshot from Go")
	}
}
