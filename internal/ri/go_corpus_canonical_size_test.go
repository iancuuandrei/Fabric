package ri

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

func TestGoCorpusCanonicalJSONSizeMatchesNormalization(t *testing.T) {
	alias := "html<&>"
	values := []any{
		GoEngineeringGraph{
			Schema: "go-engineering-graph-v1", SourceID: strings.Repeat("a", 64),
			CandidateID: strings.Repeat("b", 64), ProducerSHA256: strings.Repeat("c", 64),
			SourceDigest: strings.Repeat("d", 64), Coverage: "PARTIAL",
			Files: []GoGraphFile{{
				Facts:   GoFileFacts{Path: "pkg/with<&>/file.go", Declarations: []GoSymbol{{Name: "line\u2028sep", Kind: "function_declaration", Range: GoRange{StartByte: 0, EndByte: 8}}}},
				Package: GoPackageBinding{PackageName: "pkg\u2029name", PackageIdentity: "source-local"},
			}},
			Generators: []GoGeneratorBinding{},
			Nodes:      []GoGraphNode{{ID: strings.Repeat("e", 64), Kind: "symbol", Label: "<>& \u2028 \u2029 \\u2028 \"quoted\"\n"}},
			Edges:      []GoGraphEdge{{From: strings.Repeat("e", 64), To: strings.Repeat("f", 64), Relation: "DECLARES", Alias: &alias}},
		},
		struct {
			ASCII string `json:"ascii"`
			Text  string `json:"text"`
		}{ASCII: "plain", Text: "<>& \u2028 \u2029 \\u2028 \\ \t\r\n"},
	}
	for index, value := range values {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			want, err := canonical.Normalize(raw)
			if err != nil {
				t.Fatal(err)
			}
			got, err := goCorpusCanonicalJSONSize(value)
			if err != nil {
				t.Fatal(err)
			}
			if got != len(want) {
				t.Fatalf("canonical JSON size differs: got=%d want=%d", got, len(want))
			}
		})
	}
}

func TestGoCorpusCanonicalSizeAdmissionBoundary(t *testing.T) {
	// The production admission check compares this computed graph size with
	// goCorpusMaxGraphBytes after validating the typed graph. Use a minimal
	// JSON-shaped payload to exercise the exact byte boundary without building
	// a large graph fixture whose structural details are unrelated to sizing.
	type payload struct {
		Content string `json:"content"`
	}

	baseSize, err := goCorpusCanonicalJSONSize(payload{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		size int
		want bool
	}{
		{name: "one byte below", size: goCorpusMaxGraphBytes - 1, want: true},
		{name: "exact limit", size: goCorpusMaxGraphBytes, want: true},
		{name: "one byte above", size: goCorpusMaxGraphBytes + 1, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			contentBytes := test.size - baseSize
			if contentBytes < 0 {
				t.Fatalf("test size %d is smaller than payload overhead %d", test.size, baseSize)
			}
			gotSize, err := goCorpusCanonicalJSONSize(payload{Content: strings.Repeat("x", contentBytes)})
			if err != nil {
				t.Fatal(err)
			}
			if gotSize != test.size {
				t.Fatalf("canonical size=%d, want exact boundary size=%d", gotSize, test.size)
			}
			gotAdmitted := gotSize <= goCorpusMaxGraphBytes
			if gotAdmitted != test.want {
				t.Fatalf("admission at %d bytes=%t, want %t (limit=%d)", gotSize, gotAdmitted, test.want, goCorpusMaxGraphBytes)
			}
		})
	}
}
