package codexruntime

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexrpc"
	"harness.local/engorch/internal/ri"
)

func TestLargeLexicalRecordPrecedesDispatchAndResumesOffline(t *testing.T) {
	source := sourceAdapter(t).Source
	identity, err := ri.FromRepository(*source)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	manifest := ri.LexicalManifest{Version: 1, Source: identity, Files: []ri.LexicalFile{}}
	for n := 0; n < 8000; n++ {
		manifest.Files = append(manifest.Files, ri.LexicalFile{Path: fmt.Sprintf("src/%06d.rs", n), Blob: strings.Repeat("a", 40), SHA256: strings.Repeat("b", 64), Bytes: 3})
	}
	id, err := manifest.ID()
	if err != nil {
		t.Fatal(err)
	}
	build := ri.LexicalBuild{ManifestID: id, Shards: []ri.LexicalShard{}}
	for n := 0; n < 8; n++ {
		build.Shards = append(build.Shards, ri.LexicalShard{Directory: fmt.Sprintf("shard-%06d", n), Files: 1000, Hashes: [3]string{strings.Repeat("c", 64), strings.Repeat("c", 64), strings.Repeat("c", 64)}})
	}
	binding := LexicalBinding{Base: ri.LexicalRef{ManifestPath: filepath.Join(root, "missing.jsonl"), SourceRoot: root, IndexPath: filepath.Join(root, "index"), Manifest: manifest, Build: build}, Executable: filepath.Join(root, "ri.exe"), ExecutableSHA256: strings.Repeat("d", 64)}
	if _, err := canonical.Bytes(binding); err == nil {
		t.Fatal("fixture must exceed inline limit")
	}
	path := filepath.Join(root, "runtime.jsonl")
	failures := make(chan string, 2)
	client, done := peer(t, func(message codexrpc.Message) (any, bool) {
		if message.Method == "thread/start" {
			state, err := Inspect(path)
			if err != nil || state.LexicalRecord == nil || state.Lexical != nil {
				failures <- "compact binding missing before dispatch"
			}
			return threadResponse(root, "explicit-model"), false
		}
		return map[string]any{"turn": completedTurn()}, false
	})
	adapter := &Adapter{Client: client, JournalPath: path, Directory: root, Source: source, Lexical: &binding}
	defer func() { _ = adapter.Close(); <-done }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inv := invocation(t)
	if _, err := adapter.Execute(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Resume(ctx, inv.ID); err != nil {
		t.Fatal(err)
	}
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	binding.ExecutableSHA256 = strings.Repeat("e", 64)
	if _, err := adapter.Resume(ctx, inv.ID); err == nil {
		t.Fatal("changed lexical reader resumed")
	}
}
