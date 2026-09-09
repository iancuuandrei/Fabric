package gitlocal

import (
	"bytes"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestTreeObjectMatchesGit(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			if out, err := exec.Command("git", "init", "--object-format="+format, root).CombinedOutput(); err != nil {
				t.Fatalf("init: %v %s", err, out)
			}
			blob, _ := ObjectID(format, "blob", []byte("fixture"))
			empty, _ := ObjectID(format, "tree", nil)
			entries := []TreeEntry{{"foo0", "100644", blob}, {"foo", "40000", empty}, {"foo.bar", "100755", blob}, {"șir", "100644", blob}}
			original := append([]TreeEntry(nil), entries...)
			for _, input := range [][]TreeEntry{entries, {}} {
				data, id, err := TreeObject(format, input)
				if err != nil {
					t.Fatal(err)
				}
				var lines bytes.Buffer
				for _, e := range input {
					kind := "blob"
					if e.Mode == "40000" {
						kind = "tree"
					}
					fmt.Fprintf(&lines, "%s %s %s\t%s%c", e.Mode, kind, e.ObjectID, e.Name, 0)
				}
				cmd := exec.Command("git", "-C", root, "mktree", "--missing", "-z")
				cmd.Stdin = &lines
				out, err := cmd.CombinedOutput()
				if err != nil || strings.TrimSpace(string(out)) != id {
					t.Fatalf("mktree: %v Git %s encoder %s", err, out, id)
				}
				stored, err := exec.Command("git", "-C", root, "cat-file", "tree", id).Output()
				if err != nil || !bytes.Equal(stored, data) {
					t.Fatalf("tree bytes mismatch: %v", err)
				}
			}
			if !reflect.DeepEqual(entries, original) {
				t.Fatal("caller entries mutated")
			}
		})
	}
}

func TestTreeObjectRejectsAmbiguousEntries(t *testing.T) {
	id := strings.Repeat("a", 40)
	for _, name := range []string{"", "a/b", ".git", ".GiT", "../a", "a\x00b", "a\\b", "CON", "a."} {
		if _, _, err := TreeObject("sha1", []TreeEntry{{name, "100644", id}}); err == nil {
			t.Fatal("invalid name admitted", name)
		}
	}
	for _, entries := range [][]TreeEntry{
		{{"a", "120000", id}}, {{"a", "160000", id}}, {{"a", "100644", "bad"}},
		{{"a", "100644", id}, {"a", "40000", id}},
		{{"a", "100644", id}, {"A", "100644", id}},
	} {
		if _, _, err := TreeObject("sha1", entries); err == nil {
			t.Fatal("invalid entries admitted", entries)
		}
	}
	if _, _, err := TreeObject("sha512", nil); err == nil {
		t.Fatal("unknown format accepted")
	}
	if _, _, err := TreeObject("sha1", make([]TreeEntry, 4097)); err == nil {
		t.Fatal("oversized tree accepted")
	}
}
