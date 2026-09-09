package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestListSourcePagesGlobalOrderFromCommit(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if b, e := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); e != nil {
			t.Fatal(e, string(b))
		}
	}
	run("init", "-q")
	for _, path := range []string{"a/file", "a.txt", "z"} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if e := os.MkdirAll(filepath.Dir(full), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(full, []byte(path), 0600); e != nil {
			t.Fatal(e)
		}
	}
	run("add", ".")
	run("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	i, e := Discover(context.Background(), root, "listing-fixture")
	if e != nil {
		t.Fatal(e)
	}
	if e := os.Remove(filepath.Join(root, "a.txt")); e != nil {
		t.Fatal(e)
	}
	var paths []string
	after := ""
	for {
		page, e := ListSource(context.Background(), i, after, 1)
		if e != nil {
			t.Fatal(e)
		}
		if page.Commit != i.Commit || len(page.Entries) != 1 {
			t.Fatal(page)
		}
		paths = append(paths, page.Entries[0].Path)
		if page.NextAfter == nil {
			break
		}
		after = *page.NextAfter
	}
	if strings.Join(paths, ",") != "a.txt,a/file,z" {
		t.Fatal(paths)
	}
	var streamed []string
	if err := VisitSource(context.Background(), i, func(e SourceEntry) error {
		if e.Kind != "file" || len(e.Object) != len(i.Commit) {
			t.Fatal("invalid tree visitor binding")
		}
		streamed = append(streamed, e.Path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(streamed)
	if strings.Join(streamed, ",") != strings.Join(paths, ",") {
		t.Fatal("stream differs from committed listing")
	}
	if VisitSource(context.Background(), i, func(SourceEntry) error { return errors.New("fixture abort") }) == nil {
		t.Fatal("visitor failure reported success")
	}
	digested := 0
	if err := VisitSourceDigests(context.Background(), i, func(entry SourceEntry, digest *SourceDigest) error {
		want := sha256.Sum256([]byte(entry.Path))
		if digest == nil || digest.SHA256 != hex.EncodeToString(want[:]) || digest.Blob != entry.Object || digest.Commit != i.Commit || digest.Bytes != int64(len(entry.Path)) {
			t.Fatal("incorrect committed batch digest")
		}
		digested++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if digested != 3 {
		t.Fatal("incomplete batch observation")
	}
	if VisitSourceDigests(context.Background(), i, func(SourceEntry, *SourceDigest) error { return errors.New("fixture abort") }) == nil {
		t.Fatal("batch callback failure admitted")
	}
	writers := map[string]*batchFixtureWriter{}
	if err := CopySourceBatch(context.Background(), i, func(entry SourceEntry, size int64) (io.WriteCloser, error) {
		writer := &batchFixtureWriter{}
		writers[entry.Path] = writer
		return writer, nil
	}, func(entry SourceEntry, digest *SourceDigest) error {
		writer := writers[entry.Path]
		if writer == nil || !writer.closed || writer.String() != entry.Path || digest == nil {
			return errors.New("batch destination not closed or exact")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	failed := &batchFixtureWriter{fail: true}
	if err := CopySourceBatch(context.Background(), i, func(SourceEntry, int64) (io.WriteCloser, error) { return failed, nil }, func(SourceEntry, *SourceDigest) error { return errors.New("must not deliver failed copy") }); err == nil || !failed.closed {
		t.Fatal("failed writer leaked or admitted")
	}
}

type batchFixtureWriter struct {
	bytes.Buffer
	closed, fail bool
}

func (w *batchFixtureWriter) Write(b []byte) (int, error) {
	if w.fail {
		return 0, errors.New("fixture write failure")
	}
	return w.Buffer.Write(b)
}
func (w *batchFixtureWriter) Close() error { w.closed = true; return nil }

func TestTreeVisitorRetainsNoListingAndHonorsCeiling(t *testing.T) {
	count := 0
	sink := &treeSink{oidLength: 40, maxCount: 2, visit: func(SourceEntry) error { count++; return nil }}
	record := "100644 blob " + strings.Repeat("a", 40) + "\tfile\x00"
	if _, err := sink.Write([]byte(record + record)); err != nil {
		t.Fatal(err)
	}
	if count != 2 || len(sink.entries) != 0 {
		t.Fatal("visitor accumulated listing")
	}
	if _, err := sink.Write([]byte(record)); err == nil || count != 2 {
		t.Fatal("visitor exceeded bound")
	}
}

func TestTreeSinkChunkBoundariesKindsAndBounds(t *testing.T) {
	s := &treeSink{limit: 4, oidLength: 40}
	for _, r := range []string{"160000 commit " + strings.Repeat("a", 40) + "\tmodule\x00", "120000 blob " + strings.Repeat("b", 40) + "\tlink\x00"} {
		for _, b := range []byte(r) {
			if _, e := s.Write([]byte{b}); e != nil {
				t.Fatal(e)
			}
		}
	}
	if len(s.entries) != 2 || s.entries[0].Kind != "symlink" || s.entries[1].Kind != "submodule" {
		t.Fatal(s.entries)
	}
	if _, e := s.Write([]byte(strings.Repeat("x", 4201))); e == nil {
		t.Fatal("accepted oversized record")
	}
	s = &treeSink{limit: 1, oidLength: 40, count: 200000}
	if e := s.accept("100644 blob " + strings.Repeat("a", 40) + "\tx"); e == nil {
		t.Fatal("accepted oversized tree")
	}
}
