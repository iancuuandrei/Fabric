package journal

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

func TestFastDigestAndLineLenMatchesCanonicalExactly(t *testing.T) {
	payload, err := canonical.Bytes(map[string]any{
		"objective": "Verify exact fast digest equivalence",
		"files":     []string{"a.go", "b.go"},
		"nested":    map[string]any{"ok": true, "n": 42},
	})
	if err != nil {
		t.Fatal(err)
	}
	e := Event{
		Version:  1,
		Sequence: 7,
		Previous: "0000000000000000000000000000000000000000000000000000000000000000",
		Kind:     "files.observed",
		Payload:  payload,
	}
	wantHash, err := digest(e)
	if err != nil {
		t.Fatal(err)
	}
	e.Hash = wantHash
	wantLine, err := canonical.Bytes(e)
	if err != nil {
		t.Fatal(err)
	}
	gotHash, gotLen, err := fastDigestAndLineLen(e)
	if err != nil {
		t.Fatal(err)
	}
	if gotHash != wantHash {
		t.Fatalf("hash mismatch: got %s want %s", gotHash, wantHash)
	}
	if gotLen != len(wantLine) {
		t.Fatalf("line length mismatch: got %d want %d", gotLen, len(wantLine))
	}
}

func BenchmarkSQLiteJournalRead50Events(b *testing.B) {
	path := filepath.Join(b.TempDir(), "bench.jsonl")
	store, err := OpenSQLite(path)
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	for i := 0; i < 50; i++ {
		if _, err := store.Append("bench.event", map[string]any{
			"index": i,
			"note":  fmt.Sprintf("payload-%03d-with-representative-metadata", i),
			"items": []string{"alpha", "beta", "gamma", "delta"},
		}, func([]Event) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		events, err := store.Read()
		if err != nil || len(events) != 50 {
			b.Fatalf("read failed: len=%d err=%v", len(events), err)
		}
	}
}
