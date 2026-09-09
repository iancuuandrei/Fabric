package gitlocal

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestBuildTreesMatchesGitIndex(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			run := func(args ...string) string {
				t.Helper()
				out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("Git %v: %v %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			run("init", "--object-format="+format)
			id, _ := ObjectID(format, "blob", []byte("fixture"))
			files := []BlobEntry{{"z/deep/file", id, false}, {"foo0", id, false}, {"foo/bar", id, true}, {"foo.bar", id, false}, {"șir/file", id, false}}
			for _, f := range files {
				mode := "100644"
				if f.Executable {
					mode = "100755"
				}
				run("update-index", "--add", "--cacheinfo", mode, f.ObjectID, f.Path)
			}
			expected := run("write-tree", "--missing-ok")
			trees, err := BuildTrees(format, files)
			if err != nil || len(trees) != 5 || trees[len(trees)-1].Path != "" || trees[len(trees)-1].ObjectID != expected {
				t.Fatalf("tree construction mismatch: %v %+v expected %s", err, trees, expected)
			}
			for _, tree := range trees {
				out, err := exec.Command("git", "-C", root, "cat-file", "tree", tree.ObjectID).Output()
				if err != nil || string(out) != string(tree.Data) {
					t.Fatalf("directory %q differs from Git: %v", tree.Path, err)
				}
			}
			for i, j := 0, len(files)-1; i < j; i, j = i+1, j-1 {
				files[i], files[j] = files[j], files[i]
			}
			reversed, err := BuildTrees(format, files)
			if err != nil || !reflect.DeepEqual(trees, reversed) {
				t.Fatal("input ordering changed result", err)
			}
			empty, err := BuildTrees(format, nil)
			emptyID, _ := ObjectID(format, "tree", nil)
			if err != nil || len(empty) != 1 || empty[0].ObjectID != emptyID {
				t.Fatal("empty root mismatch", err)
			}
		})
	}
}

func TestBuildTreesRejectsConflicts(t *testing.T) {
	id := strings.Repeat("a", 40)
	for _, paths := range [][]string{{"a", "a"}, {"a", "a/b"}, {"a/b", "a"}, {"A/x", "a/y"}, {"a/x", "a/X"}, {"a/.git/x"}, {"../x"}} {
		files := []BlobEntry{}
		for _, path := range paths {
			files = append(files, BlobEntry{Path: path, ObjectID: id})
		}
		if _, err := BuildTrees("sha1", files); err == nil {
			t.Fatal("conflicting paths admitted", paths)
		}
	}
	if _, err := BuildTrees("sha512", nil); err == nil {
		t.Fatal("unknown format admitted")
	}
}
