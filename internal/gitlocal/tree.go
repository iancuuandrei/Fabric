package gitlocal

import (
	"bytes"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"harness.local/engorch/internal/safepath"
)

// TreeEntry describes one immediate child, not a slash-separated full path.
// Supported modes exclude symbolic links and submodules, matching workspaces.
type TreeEntry struct {
	Name     string
	Mode     string
	ObjectID string
}

// TreeObject encodes a bounded tree without changing the caller's entries or Git.
// Child object existence and correspondence to candidate bytes are caller duties.
func TreeObject(format string, entries []TreeEntry) ([]byte, string, error) {
	if len(entries) > 4096 {
		return nil, "", errors.New("tree entry bound exceeded")
	}
	ordered := append([]TreeEntry(nil), entries...)
	seen := map[string]bool{}
	for _, entry := range ordered {
		if strings.Contains(entry.Name, "/") || safepath.Relative(entry.Name) != nil || strings.EqualFold(entry.Name, ".git") {
			return nil, "", errors.New("invalid tree child name")
		}
		if entry.Mode != "100644" && entry.Mode != "100755" && entry.Mode != "40000" {
			return nil, "", errors.New("unsupported tree entry mode")
		}
		if !validObjectID(format, entry.ObjectID) {
			return nil, "", errors.New("invalid tree child object ID")
		}
		key := strings.ToLower(entry.Name)
		if seen[key] {
			return nil, "", errors.New("duplicate or case-colliding tree child")
		}
		seen[key] = true
	}
	// Git compares directory names as if followed by '/', not a NUL terminator.
	key := func(e TreeEntry) string {
		if e.Mode == "40000" {
			return e.Name + "/"
		}
		return e.Name
	}
	sort.Slice(ordered, func(i, j int) bool { return key(ordered[i]) < key(ordered[j]) })
	var body bytes.Buffer
	for _, entry := range ordered {
		body.WriteString(entry.Mode + " " + entry.Name)
		body.WriteByte(0)
		raw, _ := hex.DecodeString(entry.ObjectID) // validated above
		body.Write(raw)
	}
	data := body.Bytes()
	id, err := ObjectID(format, "tree", data)
	return data, id, err
}
