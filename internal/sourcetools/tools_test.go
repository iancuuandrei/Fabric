package sourcetools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
)

func TestCatalogPreservesSourceSchemas(t *testing.T) {
	got, err := canonical.Bytes(Catalog())
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"Description":"List paths from the fixed source commit. limit must be 1 to 128; use after empty string initially. Follow next_after until null; includes symlinks/submodules as explicit kinds. No relevance filtering.","InputSchema":{"additionalProperties":false,"properties":{"after":{"type":"string"},"limit":{"maximum":128,"minimum":1,"type":"integer"}},"required":["after","limit"],"type":"object"},"Name":"source_list"},{"Description":"Read exact regular-file bytes from the fixed source commit. limit must be 1 to 32768 bytes. For valid UTF-8 pages, content_utf8 contains the bytes and content_base64 is omitted; binary pages include content_base64 and null content_utf8. chunk_hash binds returned bytes. Follow next_offset until null. Working tree changes do not affect this source.","InputSchema":{"additionalProperties":false,"properties":{"limit":{"maximum":32768,"minimum":1,"type":"integer"},"offset":{"minimum":0,"type":"integer"},"path":{"type":"string"}},"required":["path","offset","limit"],"type":"object"},"Name":"source_read"}]`
	if string(got) != want {
		t.Fatalf("catalog changed\n got: %s\nwant: %s", got, want)
	}
}

func TestLegacyCatalogPreservesV1Description(t *testing.T) {
	got, err := canonical.Bytes(LegacyCatalog())
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"Description":"List paths from the fixed source commit. limit must be 1 to 128; use after empty string initially. Follow next_after until null; includes symlinks/submodules as explicit kinds. No relevance filtering.","InputSchema":{"additionalProperties":false,"properties":{"after":{"type":"string"},"limit":{"maximum":128,"minimum":1,"type":"integer"}},"required":["after","limit"],"type":"object"},"Name":"source_list"},{"Description":"Read exact regular-file bytes from the fixed source commit. limit must be 1 to 32768 bytes. Returns content_utf8 when valid UTF-8 and always content_base64, with chunk hash. Follow next_offset until null. Working tree changes do not affect this source.","InputSchema":{"additionalProperties":false,"properties":{"limit":{"maximum":32768,"minimum":1,"type":"integer"},"offset":{"minimum":0,"type":"integer"},"path":{"type":"string"}},"required":["path","offset","limit"],"type":"object"},"Name":"source_read"}]`
	if string(got) != want {
		t.Fatalf("legacy source catalog changed\n got: %s\nwant: %s", got, want)
	}
}

func TestExecuteReadsOnlyRecordedCommitAndReportsPathKinds(t *testing.T) {
	root := t.TempDir()
	run := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	run("", "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("committed"), 0600); err != nil {
		t.Fatal(err)
	}
	run("", "add", "source.txt")
	run("", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	base := run("", "rev-parse", "HEAD")
	linkBlob := run("source.txt", "hash-object", "-w", "--stdin")
	run("", "update-index", "--add", "--cacheinfo", "120000,"+linkBlob+",source-link")
	run("", "update-index", "--add", "--cacheinfo", "160000,"+base+",nested-repository")
	run("", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "path kinds")

	source, err := repository.Discover(context.Background(), root, "source-tools-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}

	content, handled, err := Execute(context.Background(), source, ListName, json.RawMessage(`{"after":"","limit":128}`))
	if err != nil || !handled {
		t.Fatal(handled, err)
	}
	page := content.(repository.SourcePage)
	kinds := map[string]string{}
	for _, entry := range page.Entries {
		kinds[entry.Path] = entry.Kind
	}
	if kinds["source.txt"] != "file" || kinds["source-link"] != "symlink" || kinds["nested-repository"] != "submodule" {
		t.Fatal(kinds)
	}

	content, handled, err = Execute(context.Background(), source, ReadName, json.RawMessage(`{"path":"source.txt","offset":0,"limit":32}`))
	if err != nil || !handled {
		t.Fatal(handled, err)
	}
	chunk := content.(ReadResult)
	if chunk.ContentBase64 != "" || chunk.ContentUTF8 == nil || *chunk.ContentUTF8 != "committed" || chunk.RepositoryID == "" || chunk.Commit != source.Commit {
		t.Fatal(chunk)
	}
	for _, path := range []string{"source-link", "nested-repository"} {
		_, handled, err = Execute(context.Background(), source, ReadName, mustArguments(t, ReadArgs{Path: path, Offset: 0, Limit: 1}))
		if err == nil || !handled {
			t.Fatal("unsupported path kind read accepted", path, handled)
		}
	}
	for _, arguments := range []json.RawMessage{
		json.RawMessage(`{"after":"","limit":0}`),
		json.RawMessage(`{"path":"source.txt","offset":-1,"limit":1}`),
		json.RawMessage(`{"path":"source.txt","offset":0,"limit":32769}`),
	} {
		name := ReadName
		if string(arguments) == `{"after":"","limit":0}` {
			name = ListName
		}
		if _, handled, err := Execute(context.Background(), source, name, arguments); err == nil || !handled {
			t.Fatal("out-of-bounds arguments accepted", string(arguments), handled)
		}
	}
	if content, handled, err := Execute(context.Background(), source, "other", json.RawMessage(`{}`)); err != nil || handled || content != nil {
		t.Fatal("unknown tool handled", content, handled, err)
	}
}

func TestReadResultUTF8PageByteProjectionAndBinaryFallback(t *testing.T) {
	text := strings.Repeat("a", 32768)
	next := int64(len(text))
	chunk := repository.SourceChunk{RepositoryID: strings.Repeat("1", 64), Commit: strings.Repeat("2", 40), Path: "src/file.go", Blob: strings.Repeat("3", 40), Offset: 0, TotalBytes: int64(len(text)) * 2, ContentBase64: base64.StdEncoding.EncodeToString([]byte(text)), ContentUTF8: &text, ChunkHash: strings.Repeat("4", 64), NextOffset: &next}
	full, err := canonical.Bytes(chunk)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := readResult(chunk)
	if err != nil {
		t.Fatal(err)
	}
	compact, err := canonical.Bytes(projected)
	if err != nil {
		t.Fatal(err)
	}
	if projected.ContentBase64 != "" || projected.ContentUTF8 == nil || *projected.ContentUTF8 != text || projected.RepositoryID != chunk.RepositoryID || projected.Commit != chunk.Commit || projected.Path != chunk.Path || projected.Blob != chunk.Blob || projected.ChunkHash != chunk.ChunkHash || projected.Offset != chunk.Offset || projected.TotalBytes != chunk.TotalBytes || projected.NextOffset == nil || *projected.NextOffset != next {
		t.Fatal("UTF-8 source projection lost exact text or binding metadata", projected)
	}
	if len(full) != 76836 || len(compact) != 33124 || len(full)-len(compact) != 43712 {
		t.Fatalf("unexpected source page payload sizes: full=%d compact=%d saved=%d", len(full), len(compact), len(full)-len(compact))
	}
	t.Logf("utf8_source_page_bytes=%d full_json_bytes=%d model_json_bytes=%d saved_bytes=%d", len(text), len(full), len(compact), len(full)-len(compact))

	binary := []byte{0x00, 0xff, 0x61}
	binaryChunk := chunk
	binaryChunk.TotalBytes = int64(len(binary))
	binaryChunk.ContentBase64 = base64.StdEncoding.EncodeToString(binary)
	binaryChunk.ContentUTF8 = nil
	binaryResult, err := readResult(binaryChunk)
	if err != nil || binaryResult.ContentBase64 != binaryChunk.ContentBase64 || binaryResult.ContentUTF8 != nil {
		t.Fatal("binary source page did not retain Base64", binaryResult, err)
	}
}

func TestReadResultRejectsMismatchedTextAndBytes(t *testing.T) {
	text := "candidate/source text"
	chunk := repository.SourceChunk{ContentBase64: base64.StdEncoding.EncodeToString([]byte("different")), ContentUTF8: &text}
	if _, err := readResult(chunk); err == nil {
		t.Fatal("mismatched text and Base64 accepted")
	}
}

func TestExecuteRejectsUnknownArgumentsBeforeRepositoryAccess(t *testing.T) {
	if _, handled, err := Execute(context.Background(), repository.Identity{}, ListName, json.RawMessage(`{"after":"","limit":1,"extra":true}`)); err == nil || !handled {
		t.Fatal("unknown argument accepted", handled)
	}
}

func mustArguments(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := canonical.Bytes(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
