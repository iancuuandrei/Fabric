package gitlocal

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestObjectIDMatchesGit(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			if out, err := exec.Command("git", "init", "--object-format="+format, root).CombinedOutput(); err != nil {
				t.Fatalf("init: %v: %s", err, out)
			}
			for _, kind := range []string{"blob", "tree", "commit"} {
				for _, data := range [][]byte{{}, []byte("binary\x00payload\xff\n"), []byte("șir UTF-8\n")} {
					cmd := exec.Command("git", "-C", root, "hash-object", "--literally", "-t", kind, "--stdin")
					cmd.Stdin = bytes.NewReader(data)
					out, err := cmd.CombinedOutput()
					if err != nil {
						t.Fatalf("hash-object: %v: %s", err, out)
					}
					got, err := ObjectID(format, kind, data)
					if err != nil || got != strings.TrimSpace(string(out)) {
						t.Fatalf("%s %s: got %s, Git %s, error %v", format, kind, got, out, err)
					}
				}
			}
		})
	}
}

func TestObjectIDRejectsUnsupportedContracts(t *testing.T) {
	for _, pair := range [][2]string{{"sha512", "blob"}, {"sha1", "tag"}, {"sha256", "blob\x00"}} {
		if _, err := ObjectID(pair[0], pair[1], nil); err == nil {
			t.Fatal("unsupported contract accepted", pair)
		}
	}
	for _, format := range []string{"sha1", "sha256"} {
		n := 40
		if format == "sha256" {
			n = 64
		}
		if !validObjectID(format, strings.Repeat("a", n)) {
			t.Fatal("valid ID rejected")
		}
		for _, id := range []string{strings.Repeat("A", n), strings.Repeat("a", n-1), strings.Repeat("a", n+1), strings.Repeat("g", n)} {
			if validObjectID(format, id) {
				t.Fatal("invalid ID accepted")
			}
		}
	}
}
