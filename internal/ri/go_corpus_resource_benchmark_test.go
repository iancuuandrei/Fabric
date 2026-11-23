package ri

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

const goCorpusResourceBenchmarkHumanizeCommit = "a1b4e66b9a6d890e9e15e7091cf16c8032367d6e"

// BenchmarkGoCorpusResource compares the opted-in parser cache with the
// unchanged uncached collection path on a size-controlled corpus and, when
// supplied, the pinned go-humanize checkout. It does not call a model/provider.
func BenchmarkGoCorpusResource(b *testing.B) {
	client := goCorpusBenchmarkClient(b)
	b.Run("synthetic-24x64KiB", func(b *testing.B) {
		root := b.TempDir()
		fixture := makeGoCorpusResourceFixture()
		initGoCorpusBenchmarkGit(b, root, fixture)
		identity, err := repository.Discover(context.Background(), root, "go-corpus-resource-24x64k")
		if err != nil {
			b.Fatal(err)
		}
		benchmarkGoCorpusResourceCases(b, client, identity, "generated package wrappers", fixtureBytes(fixture))
	})
	b.Run("pinned-go-humanize", func(b *testing.B) {
		root := os.Getenv("ENGORCH_RI_BENCH_REPOSITORY")
		if root == "" {
			b.Skip("ENGORCH_RI_BENCH_REPOSITORY must point to the pinned go-humanize checkout")
		}
		identity, err := repository.Discover(context.Background(), root, "go-humanize-resource-benchmark")
		if err != nil {
			b.Fatal(err)
		}
		if identity.Commit != goCorpusResourceBenchmarkHumanizeCommit {
			b.Fatalf("benchmark repository commit %s does not match pinned humanize source", identity.Commit)
		}
		benchmarkGoCorpusResourceCases(b, client, identity, "ParseBytes Commaf examples and generated helpers", 0)
	})
}

func benchmarkGoCorpusResourceCases(b *testing.B, client Client, identity repository.Identity, objective string, sourceBytes int) {
	b.Helper()
	cacheRoot := b.TempDir()
	var expectedIdentity *goCorpusResourceIdentity
	loggedIdentity := false
	collect := func(cacheDir string, cached bool) GoCommittedCorpus {
		b.Helper()
		var corpus GoCommittedCorpus
		var err error
		if cached {
			corpus, err = CollectCommittedGoCorpusWithOptions(context.Background(), identity, client, cacheDir, objective, GoCorpusOptions{EnableParseCache: true})
		} else {
			corpus, err = CollectCommittedGoCorpus(context.Background(), identity, client, "", objective)
		}
		if err != nil {
			b.Fatal(err)
		}
		return corpus
	}
	checkCorpus := func(corpus GoCommittedCorpus) {
		b.Helper()
		got, err := goCorpusResourceIdentityFor(identity, client, objective, corpus)
		if err != nil {
			b.Fatal(err)
		}
		if expectedIdentity == nil {
			expectedIdentity = &got
		} else if !reflect.DeepEqual(*expectedIdentity, got) {
			b.Fatal("uncached/cold/warm runs did not return the same admitted source, graph, and context")
		}
		if !loggedIdentity {
			b.Logf("source_commit=%s source_tree=%s source_set_sha256=%s graph_digest=%s context_digest=%s source_files=%d source_bytes=%d", got.Commit, got.Tree, got.SourceSetSHA256, got.GraphDigest, got.ContextDigest, got.SourceFiles, got.SourceBytes)
			loggedIdentity = true
		}
	}
	if sourceBytes > 0 {
		b.ReportMetric(float64(sourceBytes), "fixture_source-B")
	}

	b.Run("uncached", func(b *testing.B) {
		b.ReportAllocs()
		b.StopTimer()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StartTimer()
			corpus := collect("", false)
			b.StopTimer()
			checkCorpus(corpus)
		}
	})
	b.Run("cache-cold", func(b *testing.B) {
		b.ReportAllocs()
		b.StopTimer()
		var bytesOnDisk int64
		var hits, misses int
		for i := 0; i < b.N; i++ {
			cacheDir, err := os.MkdirTemp(cacheRoot, fmt.Sprintf("cold-%d-", i))
			if err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
			corpus := collect(cacheDir, true)
			b.StopTimer()
			checkCorpus(corpus)
			cacheHits, cacheMisses := corpusCacheCounts(corpus)
			hits += cacheHits
			misses += cacheMisses
			bytesOnDisk += benchmarkCacheBytes(b, cacheDir)
		}
		b.ReportMetric(float64(hits)/float64(b.N), "cache_hits/op")
		b.ReportMetric(float64(misses)/float64(b.N), "cache_misses/op")
		b.ReportMetric(float64(bytesOnDisk)/float64(b.N), "cache_disk-B/op")
	})
	b.Run("cache-warm", func(b *testing.B) {
		b.StopTimer()
		cacheDir, err := os.MkdirTemp(cacheRoot, "warm-")
		if err != nil {
			b.Fatal(err)
		}
		prime := collect(cacheDir, true)
		if prime.ReadFiles == 0 {
			b.Fatal("warm-cache prime read no Go files")
		}
		checkCorpus(prime)
		b.ReportAllocs()
		var hits, misses int
		// Cache priming and disk-size inspection are excluded from timed work.
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StartTimer()
			corpus := collect(cacheDir, true)
			b.StopTimer()
			checkCorpus(corpus)
			cacheHits, cacheMisses := corpusCacheCounts(corpus)
			hits += cacheHits
			misses += cacheMisses
		}
		bytesOnDisk := benchmarkCacheBytes(b, cacheDir)
		b.ReportMetric(float64(hits)/float64(b.N), "cache_hits/op")
		b.ReportMetric(float64(misses)/float64(b.N), "cache_misses/op")
		b.ReportMetric(float64(bytesOnDisk), "cache_disk-B")
	})
}

type goCorpusResourceIdentity struct {
	Commit          string
	Tree            string
	SourceSetSHA256 string
	GraphDigest     string
	ContextDigest   string
	SourceFiles     int
	SourceBytes     int64
}

func goCorpusResourceIdentityFor(identity repository.Identity, client Client, objective string, corpus GoCommittedCorpus) (goCorpusResourceIdentity, error) {
	if corpus.Source.Commit != identity.Commit || corpus.Source.Tree != identity.Tree || corpus.ReadFiles != len(corpus.GraphInputs) || corpus.ReadFiles != len(corpus.Sources) {
		return goCorpusResourceIdentity{}, fmt.Errorf("benchmark corpus does not match its exact committed source identity")
	}
	sourceSetSHA256, err := canonical.Hash("harness.ri.go-resource-benchmark.sources.v1", corpus.Sources)
	if err != nil {
		return goCorpusResourceIdentity{}, err
	}
	producer := client.ExecutableHash
	if len(corpus.GraphInputs) == 0 {
		return goCorpusResourceIdentity{}, fmt.Errorf("benchmark corpus selected no Go files")
	}
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: corpus.Source.RepositoryID, ProducerSHA256: producer, Files: corpus.GraphInputs, Generators: corpus.Generators, ModuleInventory: corpus.ModuleInventory})
	if err != nil {
		return goCorpusResourceIdentity{}, err
	}
	limits := taskcontext.DefaultLimits()
	limits.MaxBytes = 24 << 10
	manifest, err := CompileGoContext(GoContextInput{ContextVersion: 2, SourceID: corpus.Source.RepositoryID, Graph: graph, Objective: objective, Files: corpus.ContextFiles, Limits: limits})
	if err != nil {
		return goCorpusResourceIdentity{}, err
	}
	var sourceBytes int64
	for _, source := range corpus.Sources {
		sourceBytes += source.Bytes
	}
	return goCorpusResourceIdentity{Commit: identity.Commit, Tree: identity.Tree, SourceSetSHA256: sourceSetSHA256, GraphDigest: graph.Digest, ContextDigest: manifest.Digest, SourceFiles: len(corpus.Sources), SourceBytes: sourceBytes}, nil
}

func makeGoCorpusResourceFixture() map[string][]byte {
	files := make(map[string][]byte, goCorpusMaxFiles)
	for i := 0; i < goCorpusMaxFiles; i++ {
		name := fmt.Sprintf("pkg/file%02d.go", i)
		prefix := []byte(fmt.Sprintf("package pkg\n\nfunc Value%02d() int { return %d }\n\n", i, i))
		comment := []byte("// bounded fixture padding keeps one useful declaration per source object\n")
		var body strings.Builder
		body.Grow(64 << 10)
		body.Write(prefix)
		for body.Len()+len(comment) <= 64<<10 {
			body.Write(comment)
		}
		if body.Len() > 64<<10 {
			body.Reset()
			body.Write(prefix)
			remaining := (64 << 10) - len(prefix)
			copies := remaining / len(comment)
			for copyIndex := 0; copyIndex < copies; copyIndex++ {
				body.Write(comment)
			}
			body.Write(bytes.Repeat([]byte(" "), (64<<10)-body.Len()))
		}
		files[name] = []byte(body.String())
	}
	return files
}

func fixtureBytes(files map[string][]byte) int {
	total := 0
	for _, content := range files {
		total += len(content)
	}
	return total
}

func corpusCacheCounts(corpus GoCommittedCorpus) (int, int) {
	hits, misses := 0, 0
	for _, input := range corpus.GraphInputs {
		if input.Facts.Cache == "hit" {
			hits++
		} else if input.Facts.Cache == "miss" {
			misses++
		}
	}
	return hits, misses
}
