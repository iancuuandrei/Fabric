package ri

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

func fixtureGoFacts(path, sourceSHA, producerSHA string) GoFileFacts {
	facts := GoFileFacts{
		Schema: goFactsSchema, Language: "go", ParserVersion: goFactsParserVersion,
		Path: path, SourceSHA256: sourceSHA, ProducerSHA256: producerSHA,
		SyntaxErrors: false, Coverage: "PARTIAL",
		Declarations: []GoSymbol{{Name: "F", Kind: "function_declaration", Range: GoRange{StartByte: 15, EndByte: 16}}},
		Imports:      []GoImport{}, Calls: []GoCall{}, GeneratedMarkers: []string{}, Cache: "miss", ParseCount: 1,
	}
	var err error
	facts.CacheKey, err = canonical.Hash("harness.ri.go-file-facts.v1", map[string]any{
		"schema": goFactsSchema, "language": "go", "parser": goFactsParserVersion,
		"path": path, "source_sha256": sourceSHA, "producer_sha256": producerSHA,
	})
	if err != nil {
		panic(err)
	}
	body := facts
	body.Cache = ""
	body.ParseCount = 0
	bodyBytes, err := canonical.Bytes(body)
	if err != nil {
		panic(err)
	}
	bodyHash := sha256.Sum256(bodyBytes)
	facts.BodySHA256 = hex.EncodeToString(bodyHash[:])
	return facts
}

func TestValidateGoFileFactsBindsSchemaSourceProducerAndSpans(t *testing.T) {
	source := []byte("package p\nfunc F() {}\n")
	sourceHash := sha256.Sum256(source)
	sourceSHA := hex.EncodeToString(sourceHash[:])
	producer := strings.Repeat("a", 64)
	valid := fixtureGoFacts("pkg/file.go", sourceSHA, producer)
	if err := validateGoFileFacts(valid, "pkg/file.go", sourceSHA, producer, source); err != nil {
		t.Fatal("valid fact rejected:", err)
	}

	cases := map[string]func(*GoFileFacts){
		"schema":           func(f *GoFileFacts) { f.Schema = "engorch.go-file-facts.v2" },
		"parser":           func(f *GoFileFacts) { f.ParserVersion = "other" },
		"path":             func(f *GoFileFacts) { f.Path = "other.go" },
		"source":           func(f *GoFileFacts) { f.SourceSHA256 = strings.Repeat("b", 64) },
		"producer":         func(f *GoFileFacts) { f.ProducerSHA256 = strings.Repeat("b", 64) },
		"cache-key":        func(f *GoFileFacts) { f.CacheKey = strings.Repeat("b", 64) },
		"body-digest":      func(f *GoFileFacts) { f.BodySHA256 = strings.Repeat("b", 64) },
		"coverage":         func(f *GoFileFacts) { f.Coverage = "COMPLETE" },
		"out-of-file-span": func(f *GoFileFacts) { f.Declarations[0].Range.EndByte = len(source) + 1 },
		"metrics":          func(f *GoFileFacts) { f.ParseCount = 0 },
		"absent-marker":    func(f *GoFileFacts) { f.GeneratedMarkers = []string{"//go:generate absent"} },
		"resolved-call": func(f *GoFileFacts) {
			f.Calls = []GoCall{{Spelling: "F", Resolution: "RESOLVED", Range: GoRange{StartByte: 15, EndByte: 16}}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			facts := valid
			facts.Declarations = append([]GoSymbol(nil), valid.Declarations...)
			facts.Calls = append([]GoCall(nil), valid.Calls...)
			mutate(&facts)
			if err := validateGoFileFacts(facts, "pkg/file.go", sourceSHA, producer, source); err == nil {
				t.Fatal("invalid fact accepted")
			}
		})
	}
}

func TestGoFactsInputBoundsRejectBeforeExecutable(t *testing.T) {
	client := Client{Executable: filepath.Join(t.TempDir(), "missing-ri"), ExecutableHash: strings.Repeat("0", 64)}
	for _, tc := range []struct {
		name   string
		path   string
		source []byte
		cache  string
	}{
		{name: "path traversal", path: "../file.go", source: []byte("package p")},
		{name: "invalid UTF-8", path: "file.go", source: []byte{0xff}},
		{name: "oversized", path: "file.go", source: make([]byte, goFactsMaxSource+1)},
		{name: "relative cache", path: "file.go", source: []byte("package p"), cache: "../cache"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := client.GoFileFacts(context.Background(), tc.path, tc.source, tc.cache); err == nil {
				t.Fatal("invalid Go facts input accepted")
			}
		})
	}
}

func TestGoFactSourceMatchingHandlesGoImportLiteralFormsAndMarkers(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spec  string
		path  string
		alias *string
	}{
		{name: "quoted", spec: `"fmt"`, path: "fmt"},
		{name: "escaped", spec: `"\x66mt"`, path: "fmt"},
		{name: "escaped UTF-8 bytes", spec: `"\xc3\xa9"`, path: "é"},
		{name: "raw", spec: "`fmt`", path: "fmt"},
		{name: "dot alias", spec: `. "fmt"`, path: "fmt", alias: ptrString(".")},
		{name: "blank alias", spec: `_ "fmt"`, path: "fmt", alias: ptrString("_")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !matchesGoImport([]byte(tc.spec), GoImport{Path: tc.path, Alias: tc.alias}) {
				t.Fatalf("valid Go import spec did not match: %q", tc.spec)
			}
		})
	}
	if matchesGoImport([]byte(`"fmt"`), GoImport{Path: "other"}) {
		t.Fatal("import path differing from source was accepted")
	}
	if matchesGoImport([]byte("`fmt`"), GoImport{Path: "fmt", Alias: ptrString(".")}) {
		t.Fatal("alias absent from source was accepted")
	}

	source := []byte("package p\n//go:generate go run ./cmd/gen\nvar text = `//go:generate fake`\n// Code generated by fixture. DO NOT EDIT.\n")
	markers := []string{"// Code generated by fixture. DO NOT EDIT.", "//go:generate go run ./cmd/gen"}
	if !matchesGoGeneratedMarkers(source, markers) {
		t.Fatal("source comment markers rejected")
	}
	if matchesGoGeneratedMarkers(source, []string{"//go:generate fake"}) {
		t.Fatal("marker found only inside a string literal accepted")
	}
}

func ptrString(value string) *string { return &value }

func TestActualRustGoFileFacts(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("set ENGORCH_RI_BINARY to built Rust RI")
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	exeHash := sha256.Sum256(binary)
	source := []byte("//go:generate go run ./cmd/gen\npackage p\nimport ( . `raw/path`; _ \"x\\x2fy\"; named \"café\"; bytes \"\\xc3\\xa9\" )\nfunc F() { named.Run() }\ntype A = B\ntype Z struct{}\n")
	client := Client{Executable: executable, ExecutableHash: hex.EncodeToString(exeHash[:])}
	facts, err := client.GoFileFacts(context.Background(), "pkg/file.go", source, "")
	if err != nil {
		t.Fatal(err)
	}
	if facts.SourceSHA256 != hex.EncodeToString(sha256Sum(source)) || facts.Path != "pkg/file.go" || facts.Cache != "miss" || facts.ParseCount != 1 || facts.Coverage != "PARTIAL" {
		t.Fatalf("unexpected Go facts binding/metrics: %+v", facts)
	}
	if len(facts.Declarations) != 3 || facts.Declarations[0].Name != "F" || len(facts.Imports) != 4 || facts.Imports[0].Path != "raw/path" || facts.Imports[1].Path != "x/y" || facts.Imports[2].Path != "café" || facts.Imports[3].Path != "é" || facts.Imports[0].Alias == nil || *facts.Imports[0].Alias != "." || facts.Imports[1].Alias == nil || *facts.Imports[1].Alias != "_" || facts.Imports[3].Alias == nil || *facts.Imports[3].Alias != "bytes" || len(facts.Calls) != 1 || facts.Calls[0].Resolution != "UNRESOLVED" || len(facts.GeneratedMarkers) != 1 {
		t.Fatalf("unexpected syntax facts: %+v", facts)
	}
	cacheDir := t.TempDir()
	cold, err := client.GoFileFacts(context.Background(), "pkg/file.go", source, cacheDir)
	if err != nil || cold.Cache != "miss" || cold.ParseCount != 1 {
		t.Fatalf("cache miss result: facts=%+v err=%v", cold, err)
	}
	warm, err := client.GoFileFacts(context.Background(), "pkg/file.go", source, cacheDir)
	if err != nil || warm.Cache != "hit" || warm.ParseCount != 0 || warm.BodySHA256 != cold.BodySHA256 || warm.CacheKey != cold.CacheKey {
		t.Fatalf("cache hit result: facts=%+v err=%v", warm, err)
	}
	cachePath := filepath.Join(cacheDir, cold.CacheKey+".json")
	forged := cold
	forged.Declarations = append([]GoSymbol(nil), cold.Declarations...)
	forged.Declarations[0].Name = "Forged"
	forgedBody := forged
	forgedBody.BodySHA256 = ""
	forgedBody.Cache = ""
	forgedBody.ParseCount = 0
	forgedBytes, err := canonical.Bytes(forgedBody)
	if err != nil {
		t.Fatal(err)
	}
	forgedHash := sha256.Sum256(forgedBytes)
	forged.BodySHA256 = hex.EncodeToString(forgedHash[:])
	forgedBytes, err = canonical.Bytes(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, forgedBytes, 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := client.GoFileFacts(context.Background(), "pkg/file.go", source, cacheDir)
	if err != nil || recovered.Cache != "miss" || recovered.ParseCount != 1 || recovered.Declarations[0].Name != "F" {
		t.Fatalf("forged cache fact was not rejected and recomputed: facts=%+v err=%v", recovered, err)
	}
	if err := os.WriteFile(cachePath, []byte("not a canonical fact"), 0600); err != nil {
		t.Fatal(err)
	}
	reparsed, err := client.GoFileFacts(context.Background(), "pkg/file.go", source, cacheDir)
	if err != nil || reparsed.Cache != "miss" || reparsed.ParseCount != 1 || reparsed.BodySHA256 != cold.BodySHA256 {
		t.Fatalf("corrupt cache was not recomputed: facts=%+v err=%v", reparsed, err)
	}
}

func TestActualRustStreamGoFileFactsColdWarmAndPinnedProducer(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("set ENGORCH_RI_BINARY to built Rust RI")
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(binary)
	client := Client{Executable: executable, ExecutableHash: hex.EncodeToString(hash[:])}
	stream, err := client.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	cache := t.TempDir()
	source := []byte("package p\nfunc F() {}\n")
	cold, err := stream.GoFileFacts(context.Background(), "pkg/file.go", source, cache)
	if err != nil || cold.Cache != "miss" || cold.ParseCount != 1 {
		t.Fatalf("stream cache miss: facts=%+v err=%v", cold, err)
	}
	warm, err := stream.GoFileFacts(context.Background(), "pkg/file.go", source, cache)
	if err != nil || warm.Cache != "hit" || warm.ParseCount != 0 || warm.BodySHA256 != cold.BodySHA256 || warm.CacheKey != cold.CacheKey || warm.ProducerSHA256 != client.ExecutableHash {
		t.Fatalf("stream cache hit or producer binding: facts=%+v err=%v", warm, err)
	}
	bad := Client{Executable: executable, ExecutableHash: strings.Repeat("0", 64)}
	if _, err := bad.OpenStream(context.Background()); err == nil {
		t.Fatal("stream accepted a substituted producer digest")
	}
}

func sha256Sum(value []byte) []byte {
	digest := sha256.Sum256(value)
	return digest[:]
}
