package ri

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
)

func TestLexicalRecordsLargeAndRejectedPartialScopes(t *testing.T) {
	m := LexicalManifest{Version: 1, Source: Source{strings.Repeat("a", 64), "sha1", strings.Repeat("b", 40), strings.Repeat("c", 40)}}
	for n := 0; n < 8000; n++ {
		m.Files = append(m.Files, LexicalFile{Path: fmt.Sprintf("src/%06d.rs", n), Blob: strings.Repeat("d", 40), SHA256: strings.Repeat("e", 64), Bytes: 3})
	}
	var buffer bytes.Buffer
	if err := m.WriteRecords(&buffer); err != nil {
		t.Fatal(err)
	}
	raw := buffer.Bytes()
	if len(raw) <= 1<<20 {
		t.Fatal("fixture must exceed inline limit")
	}
	id, err := m.ID()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadLexicalRecords(context.Background(), bytes.NewReader(raw), m.Source, id)
	if err != nil || len(got.Files) != 8000 {
		t.Fatal("large manifest hydration", err)
	}
	path := filepath.Join(t.TempDir(), "manifest.jsonl")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ref, err := m.Reference(path)
	if err != nil {
		t.Fatal(err)
	}
	hydrated, err := ref.Hydrate(context.Background())
	if err != nil || len(hydrated.Files) != 8000 {
		t.Fatal("artifact hydration", err)
	}
	build := LexicalBuild{ManifestID: id, Shards: []LexicalShard{}}
	for n := 0; n < 8; n++ {
		build.Shards = append(build.Shards, LexicalShard{Directory: fmt.Sprintf("shard-%06d", n), Files: 1000, Hashes: [3]string{strings.Repeat("f", 64), strings.Repeat("f", 64), strings.Repeat("f", 64)}})
	}
	full := LexicalRef{ManifestPath: path, SourceRoot: filepath.Dir(path), IndexPath: filepath.Join(filepath.Dir(path), "index"), Manifest: m, Build: build}
	compact, err := full.Compact()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := canonical.Bytes(compact)
	if err != nil || len(encoded) > 4096 {
		t.Fatal("compact reference exceeded fixture budget", err)
	}
	if _, err := canonical.Bytes(full); err == nil {
		t.Fatal("fixture full reference must exceed inline limit")
	}
	reloaded, err := compact.Hydrate(context.Background())
	if err != nil || len(reloaded.Manifest.Files) != 8000 {
		t.Fatal("compact hydration", err)
	}
	badBuild := compact
	badBuild.Build.ManifestID = strings.Repeat("0", 64)
	if _, err := badBuild.Hydrate(context.Background()); err == nil {
		t.Fatal("substituted build admitted")
	}
	wrong := ref
	wrong.Files--
	if _, err := wrong.Hydrate(context.Background()); err == nil {
		t.Fatal("wrong count admitted")
	}
	if err := os.WriteFile(path, raw[:len(raw)-1], 0600); err != nil {
		t.Fatal(err)
	}
	if partial, err := ref.Hydrate(context.Background()); err == nil || len(partial.Files) != 0 {
		t.Fatal("torn artifact admitted")
	}
	missing := ref
	missing.Path = filepath.Join(filepath.Dir(path), "missing.jsonl")
	if _, err := missing.Hydrate(context.Background()); err == nil {
		t.Fatal("missing artifact admitted")
	}
	lines := bytes.Split(raw, []byte{'\n'})
	duplicate := append(append([]byte{}, raw...), lines[1]...)
	duplicate = append(duplicate, '\n')
	reordered := append(append([]byte{}, lines[1]...), '\n')
	reordered = append(reordered, raw...)
	for _, bad := range [][]byte{raw[:len(raw)-1], duplicate, reordered, append([]byte(" "), raw...), []byte("{}\n"), bytes.Repeat([]byte{'a'}, (1<<20)+2)} {
		got, err := ReadLexicalRecords(context.Background(), bytes.NewReader(bad), m.Source, id)
		if err == nil || len(got.Files) != 0 || got.Version != 0 {
			t.Fatal("partial/invalid scope admitted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadLexicalRecords(ctx, bytes.NewReader(raw), m.Source, id); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := ReadLexicalRecords(context.Background(), bytes.NewReader(raw), m.Source, strings.Repeat("0", 64)); err == nil {
		t.Fatal("foreign identity admitted")
	}
}
