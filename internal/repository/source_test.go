package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"unicode/utf8"
)

func TestCommittedSourceReadIgnoresDirtyCheckoutAndPaginates(t *testing.T) {
	root := t.TempDir()
	gitRun := func(args ...string) {
		t.Helper()
		if b, e := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); e != nil {
			t.Fatal(e, string(b))
		}
	}
	gitRun("init", "-q")
	data := []byte{'a', 0, 255, '\n', 'z'}
	if err := os.WriteFile(filepath.Join(root, "data.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	gitRun("add", "data.bin")
	gitRun("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "source")
	i, err := Discover(context.Background(), root, "source-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data.bin"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	var got []byte
	digest, err := DigestSource(context.Background(), i, "data.bin")
	if err != nil || digest.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) || digest.Bytes != int64(len(data)) || digest.Commit != i.Commit {
		t.Fatal("committed digest mismatch", digest, err)
	}
	var copied bytes.Buffer
	copyEvidence, err := CopySource(context.Background(), i, "data.bin", &copied)
	if err != nil || copyEvidence != digest || !bytes.Equal(copied.Bytes(), data) {
		t.Fatal("committed copy mismatch", err)
	}
	if _, err := CopySource(context.Background(), i, "data.bin", shortSourceWriter{}); err == nil {
		t.Fatal("short destination write accepted")
	}
	for offset := int64(0); ; {
		chunk, err := ReadSource(context.Background(), i, "data.bin", offset, 2)
		if err != nil {
			t.Fatal(err)
		}
		part, err := base64.StdEncoding.DecodeString(chunk.ContentBase64)
		if err != nil {
			t.Fatal(err)
		}
		if utf8.Valid(part) {
			if chunk.ContentUTF8 == nil || *chunk.ContentUTF8 != string(part) {
				t.Fatal("UTF-8 view differs from exact bytes")
			}
		} else if chunk.ContentUTF8 != nil {
			t.Fatal("invalid UTF-8 was exposed as text")
		}
		got = append(got, part...)
		if chunk.Commit != i.Commit || chunk.TotalBytes != int64(len(data)) {
			t.Fatal("source binding mismatch")
		}
		if chunk.NextOffset == nil {
			break
		}
		offset = *chunk.NextOffset
	}
	if string(got) != string(data) {
		t.Fatal("read working-tree or corrupted binary bytes")
	}
	for _, path := range []string{"../data.bin", "missing", "data.bin/child"} {
		if _, err := ReadSource(context.Background(), i, path, 0, 2); err == nil {
			t.Fatal("invalid source accepted", path)
		}
	}
	if _, err := ReadSource(context.Background(), i, "data.bin", 6, 2); err == nil {
		t.Fatal("offset beyond blob accepted")
	}
}

type shortSourceWriter struct{}

func (shortSourceWriter) Write(data []byte) (int, error) { return 0, io.ErrShortWrite }
