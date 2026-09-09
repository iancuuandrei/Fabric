package ri

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/gitlocal"
)

func TestStreamRejectsUncorrelatedAndInvalidFrames(t *testing.T) {
	for _, body := range []string{`{"id":2,"ok":true,"result":{},"version":1}`, `{"id":1,"ok":true,"result":null,"version":1}`, `{"error":"oops","id":1,"ok":true,"result":{},"version":1}`, `{ "id":1}`} {
		if _, err, terminal := decodeStreamResponse([]byte(body), 1); err == nil || !terminal {
			t.Fatalf("admitted %s", body)
		}
	}
	for _, size := range []uint32{0, canonical.MaxBytes + 1, 10} {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], size)
		if _, err := exchangeFrame(&bytes.Buffer{}, bytes.NewReader(header[:]), []byte(`{}`)); err == nil {
			t.Fatal("admitted invalid/truncated frame")
		}
	}
}

func TestActualRustPersistentStream(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("set ENGORCH_RI_BINARY to built Rust executable")
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	client := Client{executable, hex.EncodeToString(hash[:])}
	stream, err := client.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	source := Source{strings.Repeat("a", 64), "sha1", strings.Repeat("b", 40), strings.Repeat("c", 40)}
	request := map[string]any{"operation": "lexical_manifest", "source": source, "manifest": map[string]any{"version": 1, "source": source, "files": []any{}}}
	for i := 0; i < 3; i++ {
		current := request
		if i == 1 {
			current = map[string]any{"operation": "unsupported"}
		}
		result, err := stream.Call(context.Background(), current)
		if i == 1 {
			if err == nil {
				t.Fatal("operation error lost")
			}
		} else if err != nil || len(result) == 0 {
			t.Fatalf("request %d: %s %v", i, result, err)
		}
	}
	if stream.next != 4 {
		t.Fatal("sequence not advanced across error")
	}
	file := func(path, body string) LexicalFile {
		digest := sha256.Sum256([]byte(body))
		blob, err := gitlocal.ObjectID("sha1", "blob", []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return LexicalFile{Path: path, Blob: blob, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(body))}
	}
	base := LexicalManifest{Version: 1, Source: source, Files: []LexicalFile{file("a.txt", "needle needle"), file("b.txt", "needle"), file("docs/base.rs", "needle"), file("src/base.rs", "needle")}}
	staged, err := StageLexicalOverlay(context.Background(), LexicalManifest{Version: 1, Source: source, Files: []LexicalFile{}}, strings.Repeat("d", 64), base, []string{}, map[string][]byte{"a.txt": []byte("needle needle"), "b.txt": []byte("needle"), "docs/base.rs": []byte("needle"), "src/base.rs": []byte("needle")}, filepath.Join(t.TempDir(), "base"))
	if err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(t.TempDir(), "index")
	build, err := client.BuildLexical(context.Background(), base, staged.ManifestPath, staged.SourceRoot, index, 1024, 10)
	if err != nil {
		t.Fatal(err)
	}
	ref := LexicalRef{ManifestPath: staged.ManifestPath, SourceRoot: staged.SourceRoot, IndexPath: index, Manifest: base, Build: build}
	query := LexicalQuery{Pattern: "needle", Fixed: true}
	first, err := stream.SearchLexical(context.Background(), ref, query, 1, nil)
	if err != nil || first.Next == nil || len(first.Matches) != 1 {
		t.Fatalf("first page: %+v %v", first, err)
	}
	secondPage, err := stream.SearchLexical(context.Background(), ref, query, 10, first.Next)
	if err != nil || len(secondPage.Matches) != 4 || secondPage.Next != nil {
		t.Fatalf("second page: %+v %v", secondPage, err)
	}
	filtered := LexicalQuery{Pattern: "needle", Fixed: true, Path: "src", Type: "rs"}
	filteredPage, err := stream.SearchLexical(context.Background(), ref, filtered, 1, nil)
	if err != nil || len(filteredPage.Matches) != 1 || filteredPage.Matches[0].Path != "src/base.rs" || filteredPage.CandidateFiles != 1 || filteredPage.Next != nil {
		t.Fatalf("base filters were not applied before pagination: %+v %v", filteredPage, err)
	}
	typePage, err := stream.SearchLexical(context.Background(), ref, LexicalQuery{Pattern: "needle", Fixed: true, Type: "rs"}, 1, nil)
	if err != nil || typePage.Next == nil || typePage.Matches[0].Path != "docs/base.rs" {
		t.Fatalf("filtered cursor fixture failed: %+v %v", typePage, err)
	}
	if _, err := stream.SearchLexical(context.Background(), ref, filtered, 1, typePage.Next); err == nil {
		t.Fatal("cursor admitted across filters")
	}
	changed := LexicalManifest{Version: 1, Source: source, Files: []LexicalFile{file("a.txt", "replacement"), file("src/base.rs", "replacement"), file("src/new.rs", "needle")}}
	overlay, err := StageLexicalOverlay(context.Background(), base, strings.Repeat("e", 64), changed, []string{"b.txt"}, map[string][]byte{"a.txt": []byte("replacement"), "src/base.rs": []byte("replacement"), "src/new.rs": []byte("needle")}, filepath.Join(t.TempDir(), "overlay"))
	if err != nil {
		t.Fatal(err)
	}
	shadowed := LexicalQuery{Pattern: "needle", Fixed: true, Path: "a.txt"}
	page, err := stream.SearchLexicalOverlay(context.Background(), ref, overlay, shadowed, 10, nil)
	if err != nil || len(page.Matches) != 0 {
		t.Fatalf("shadowed match leaked: %+v %v", page, err)
	}
	overlayFiltered, err := stream.SearchLexicalOverlay(context.Background(), ref, overlay, filtered, 10, nil)
	if err != nil || len(overlayFiltered.Matches) != 1 || overlayFiltered.Matches[0].Path != "src/new.rs" || overlayFiltered.CandidateFiles != 1 {
		t.Fatalf("overlay filters or shadowing differed: %+v %v", overlayFiltered, err)
	}
	if _, err := stream.SearchLexicalOverlay(context.Background(), ref, overlay, query, 10, first.Next); err == nil {
		t.Fatal("base cursor admitted into overlay")
	}
	if err := os.WriteFile(filepath.Join(staged.SourceRoot, base.Files[0].SHA256), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.SearchLexical(context.Background(), ref, query, 10, nil); err == nil {
		t.Fatal("changed bytes reused by persistent reader")
	}
	if err := os.WriteFile(filepath.Join(staged.SourceRoot, base.Files[0].SHA256), []byte("needle needle"), 0600); err != nil {
		t.Fatal(err)
	}
	shardPath := filepath.Join(index, build.Shards[0].Directory, "lookup.bin")
	shardBytes, err := os.ReadFile(shardPath)
	if err != nil || len(shardBytes) == 0 {
		t.Fatal("missing shard bytes", err)
	}
	shard, err := os.OpenFile(shardPath, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := shard.WriteAt([]byte{shardBytes[0] ^ 1}, 0)
	closeErr := shard.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal(writeErr, closeErr)
	}
	if _, err := stream.SearchLexical(context.Background(), ref, query, 10, nil); err == nil {
		t.Fatal("changed shard reused by persistent reader")
	}
	_ = stream.Close()
	if stream.command.ProcessState == nil {
		t.Fatal("process not reaped")
	}
	if _, err := stream.Call(context.Background(), request); err == nil {
		t.Fatal("closed session reused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	second, err := client.OpenStream(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer second.Close()
	cancel()
	select {
	case <-second.done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled process not reaped")
	}
	if _, err := second.Call(context.Background(), request); err == nil {
		t.Fatal("cancelled session reused")
	}
}
