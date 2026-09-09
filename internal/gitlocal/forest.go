package gitlocal

import (
	"errors"
	"sort"
	"strings"

	"harness.local/engorch/internal/safepath"
)

// BlobEntry binds a portable repository-relative file path to a native blob ID.
// Callers must establish that the ID represents the admitted candidate bytes.
type BlobEntry struct {
	Path       string
	ObjectID   string
	Executable bool
}

// EncodedTree records one directory's native Git bytes and object identity.
type EncodedTree struct {
	Path     string
	ObjectID string
	Data     []byte
}

type treeNode struct {
	name     string
	blob     *BlobEntry
	children map[string]*treeNode
}

// BuildTrees returns every directory in deterministic child-before-parent order.
// The final entry is the root (empty Path), including for an empty file manifest.
// No filesystem reads, object writes or ref updates occur.
func BuildTrees(format string, files []BlobEntry) ([]EncodedTree, error) {
	if len(files) > 4096 {
		return nil, errors.New("manifest file bound exceeded")
	}
	root := &treeNode{children: map[string]*treeNode{}}
	for _, file := range files {
		if safepath.Relative(file.Path) != nil || !validObjectID(format, file.ObjectID) {
			return nil, errors.New("invalid blob entry")
		}
		node := root
		parts := strings.Split(file.Path, "/")
		for i, part := range parts {
			if strings.EqualFold(part, ".git") {
				return nil, errors.New("Git control path in manifest")
			}
			key := strings.ToLower(part)
			child := node.children[key]
			if child == nil {
				child = &treeNode{name: part, children: map[string]*treeNode{}}
				node.children[key] = child
			} else if child.name != part {
				return nil, errors.New("case-colliding manifest path")
			}
			if child.blob != nil {
				return nil, errors.New("duplicate or file-directory conflict")
			}
			if i == len(parts)-1 {
				if len(child.children) != 0 {
					return nil, errors.New("file-directory conflict")
				}
				copy := file
				child.blob = &copy
			}
			node = child
		}
	}
	result := []EncodedTree{}
	var encode func(*treeNode, string) (string, error)
	encode = func(node *treeNode, path string) (string, error) {
		keys := make([]string, 0, len(node.children))
		for key := range node.children {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		entries := make([]TreeEntry, 0, len(keys))
		for _, key := range keys {
			child := node.children[key]
			entry := TreeEntry{Name: child.name, Mode: "40000"}
			if child.blob != nil {
				entry.Mode = "100644"
				if child.blob.Executable {
					entry.Mode = "100755"
				}
				entry.ObjectID = child.blob.ObjectID
			} else {
				childPath := child.name
				if path != "" {
					childPath = path + "/" + child.name
				}
				var err error
				entry.ObjectID, err = encode(child, childPath)
				if err != nil {
					return "", err
				}
			}
			entries = append(entries, entry)
		}
		data, id, err := TreeObject(format, entries)
		if err != nil {
			return "", err
		}
		result = append(result, EncodedTree{Path: path, ObjectID: id, Data: data})
		return id, nil
	}
	if _, err := encode(root, ""); err != nil {
		return nil, err
	}
	return result, nil
}
