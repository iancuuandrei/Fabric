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

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/repository"
)

func TestCatalogPreservesSourceSchemas(t *testing.T) {
	got, err := canonical.Bytes(Catalog())
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"Description":"List paths from the fixed source commit. limit must be 1 to 128; use after empty string initially. Follow next_after until null; includes symlinks/submodules as explicit kinds. No relevance filtering.","InputSchema":{"additionalProperties":false,"properties":{"after":{"type":"string"},"limit":{"maximum":128,"minimum":1,"type":"integer"}},"required":["after","limit"],"type":"object"},"Name":"source_list"},{"Description":"Read exact regular-file bytes from the fixed source commit. limit must be 1 to 32768 bytes. Returns content_utf8 when valid UTF-8 and always content_base64, with chunk hash. Follow next_offset until null. Working tree changes do not affect this source.","InputSchema":{"additionalProperties":false,"properties":{"limit":{"maximum":32768,"minimum":1,"type":"integer"},"offset":{"minimum":0,"type":"integer"},"path":{"type":"string"}},"required":["path","offset","limit"],"type":"object"},"Name":"source_read"}]`
	if string(got) != want {
		t.Fatalf("catalog changed\n got: %s\nwant: %s", got, want)
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
	chunk := content.(repository.SourceChunk)
	data, err := base64.StdEncoding.DecodeString(chunk.ContentBase64)
	if err != nil || string(data) != "committed" || chunk.RepositoryID == "" || chunk.Commit != source.Commit {
		t.Fatal(string(data), chunk, err)
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
