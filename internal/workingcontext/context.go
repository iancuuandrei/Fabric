package workingcontext

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

// MaxContentBytes is the explicit initial experimental retention budget. It is
// a UTF-8 byte bound, not a tokenizer estimate or a claimed token saving.
const MaxContentBytes = 16 << 10

// MaxEncodedBytes bounds parsing before escaped JSON can allocate note content.
const MaxEncodedBytes = 128 << 10

// Binding identifies the controller-owned state from which a projection derives.
// JournalHead must identify an admitted prefix; the controller checks ancestry.
type Binding struct {
	RunID       string `json:"run_id"`
	AgentID     string `json:"agent_id"`
	TaskID      string `json:"task_id"`
	Role        string `json:"role"`
	SourceID    string `json:"source_id"`
	CandidateID string `json:"candidate_id,omitempty"`
	JournalHead string `json:"journal_head"`
}

// Context is untrusted reasoning content, separate from the authoritative
// invocation envelope. Its hashes bind bytes; they do not prove factual truth.
type Context struct {
	Version     int     `json:"version"`
	Binding     Binding `json:"binding"`
	Content     string  `json:"content"`
	ContentHash string  `json:"content_hash"`
	SizeBytes   int     `json:"size_bytes"`
	ID          string  `json:"id"`
}

// Replacement is the only model-controlled operation. It cannot name another
// agent, change bindings, erase evidence or mutate repository files.
type Replacement struct {
	ExpectedID          string `json:"expected_id"`
	ExpectedContentHash string `json:"expected_content_hash"`
	Content             string `json:"content"`
}

// Validate checks syntax only; controller provenance remains independently
// required. A syntactically valid binding is not execution permission.
func (b Binding) Validate() error {
	if !hashID(b.RunID) || !hashID(b.SourceID) || !hashID(b.JournalHead) || b.CandidateID != "" && !hashID(b.CandidateID) {
		return errors.New("working context binding identity invalid")
	}
	if !label(b.AgentID) || !label(b.TaskID) {
		return errors.New("working context agent or task identity invalid")
	}
	switch b.Role {
	case "planner", "explorer", "writer", "fixer", "reviewer":
	default:
		return errors.New("working context role invalid")
	}
	return nil
}

// New creates a bounded projection from caller-owned binding and model content.
func New(binding Binding, content string) (Context, error) {
	if err := binding.Validate(); err != nil {
		return Context{}, err
	}
	if !safeContent(content) {
		return Context{}, errors.New("working context content invalid or oversized")
	}
	digest := sha256.Sum256([]byte(content))
	c := Context{Version: 1, Binding: binding, Content: content, ContentHash: hex.EncodeToString(digest[:]), SizeBytes: len(content)}
	id, err := contextID(c)
	c.ID = id
	return c, err
}

// Validate checks exact encoding, byte count and full projection identity.
func (c Context) Validate() error {
	if c.Version != 1 {
		return errors.New("working context version unsupported")
	}
	want, err := New(c.Binding, c.Content)
	if err != nil || c.ContentHash != want.ContentHash || c.SizeBytes != want.SizeBytes || c.ID != want.ID {
		return errors.New("working context projection integrity invalid")
	}
	return nil
}

// Replace performs compare-and-swap on a valid current projection. Only the
// controller may supply the producing journal head; the ownership scope cannot
// change. A rejected replacement leaves the original value untouched.
func Replace(current Context, producingHead string, request Replacement) (Context, error) {
	if err := current.Validate(); err != nil {
		return current, err
	}
	if request.ExpectedID != current.ID || request.ExpectedContentHash != current.ContentHash {
		return current, errors.New("working context replacement preimage mismatch")
	}
	binding := current.Binding
	binding.JournalHead = producingHead
	next, err := New(binding, request.Content)
	if err != nil {
		return current, err
	}
	return next, nil
}

// Decode validates a bounded stored projection without trusting its contents.
func Decode(raw []byte) (Context, error) {
	if len(raw) > MaxEncodedBytes {
		return Context{}, errors.New("working context encoding exceeds bound")
	}
	var c Context
	if err := canonical.Decode(raw, &c); err != nil {
		return Context{}, errors.New("working context encoding invalid")
	}
	return c, c.Validate()
}

// DecodeReplacement admits only a bounded content replacement; foreign binding
// or authority fields fail strict decoding. Compare-and-swap remains separate.
func DecodeReplacement(raw []byte) (Replacement, error) {
	if len(raw) > MaxEncodedBytes {
		return Replacement{}, errors.New("working context replacement encoding exceeds bound")
	}
	var request Replacement
	if err := canonical.Decode(raw, &request); err != nil || !hashID(request.ExpectedID) || !hashID(request.ExpectedContentHash) || !safeContent(request.Content) {
		return Replacement{}, errors.New("working context replacement encoding or content invalid")
	}
	return request, nil
}

// Select returns an exact compatible projection, or an empty replacement on
// missing, stale or corrupt optional data. Invalid authoritative bindings remain
// errors. Callers must derive expected from admitted journal state, not the data.
func Select(expected Binding, raw []byte) (Context, string, error) {
	fresh, err := New(expected, "")
	if err != nil {
		return Context{}, "", err
	}
	if len(raw) == 0 {
		return fresh, "missing", nil
	}
	prior, err := Decode(raw)
	if err != nil {
		return fresh, "corrupt", nil
	}
	if prior.Binding != expected {
		return fresh, "stale", nil
	}
	return prior, "retained", nil
}

func contextID(c Context) (string, error) {
	// Omit ID itself; content bytes are bound by the independent content hash.
	return canonical.Hash("fabric.working-context.v1", struct {
		Version     int     `json:"version"`
		Binding     Binding `json:"binding"`
		ContentHash string  `json:"content_hash"`
		SizeBytes   int     `json:"size_bytes"`
	}{c.Version, c.Binding, c.ContentHash, c.SizeBytes})
}

func hashID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func label(s string) bool {
	if strings.TrimSpace(s) == "" || len(s) > 128 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func safeContent(s string) bool {
	if len(s) > MaxContentBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}
