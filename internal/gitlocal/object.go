package gitlocal

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strings"
)

// ObjectID computes Git's native object identity, including its type/size header.
// SHA-1 here implements Git compatibility; approval identities remain SHA-256.
func ObjectID(format, kind string, data []byte) (string, error) {
	if kind != "blob" && kind != "tree" && kind != "commit" {
		return "", errors.New("unsupported Git object kind")
	}
	if len(data) > 64<<20 {
		return "", errors.New("Git object exceeds local bound")
	}
	var digest hash.Hash
	switch format {
	case "sha1":
		digest = sha1.New()
	case "sha256":
		digest = sha256.New()
	default:
		return "", errors.New("unsupported Git object format")
	}
	_, _ = fmt.Fprintf(digest, "%s %d\x00", kind, len(data))
	_, _ = digest.Write(data)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validObjectID(format, id string) bool {
	size := 40
	if format == "sha256" {
		size = 64
	} else if format != "sha1" {
		return false
	}
	return len(id) == size && strings.Trim(id, "0123456789abcdef") == ""
}

// CommitObject encodes an unsigned single-parent commit with exact UTC metadata.
// The supplied tree ID must later be proven to represent the plan's file manifest;
// encoding alone does not prove that tree or commit objects exist in Git storage.
func CommitObject(plan CommitPlan, treeID string) ([]byte, string, error) {
	if _, err := plan.ID(); err != nil {
		return nil, "", err
	}
	format := plan.Workspace.Request.Source.ObjectFormat
	if !validObjectID(format, treeID) || !validObjectID(format, plan.Candidate.Head) {
		return nil, "", errors.New("commit tree/parent object format mismatch")
	}
	data := []byte(fmt.Sprintf("tree %s\nparent %s\nauthor %s <%s> %d +0000\ncommitter %s <%s> %d +0000\n\n%s", treeID, plan.Candidate.Head, plan.Author.Name, plan.Author.Email, plan.Author.UnixSeconds, plan.Committer.Name, plan.Committer.Email, plan.Committer.UnixSeconds, plan.Message))
	id, err := ObjectID(format, "commit", data)
	return data, id, err
}
