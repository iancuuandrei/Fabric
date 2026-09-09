package codexruntime

import (
	"errors"
	"path/filepath"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/ri"
	"harness.local/engorch/internal/safepath"
)

// LexicalBinding fixes controller-selected lexical artifacts before model access.
// Validation binds metadata; controller artifact readback and immutable ownership
// remain required. A model must never supply these paths or identities as tools.
type LexicalBinding struct {
	Base             ri.LexicalRef         `json:"base"`
	Overlay          *ri.LexicalOverlayRef `json:"overlay"`
	Executable       string                `json:"executable"`
	ExecutableSHA256 string                `json:"executable_sha256"`
}

// Validate binds base source and optional overlay to the admitted runtime candidate.
// An empty candidate permits only base search, never a candidate overlay.
func (b LexicalBinding) Validate(source repository.Identity, candidate string) error {
	expected, err := ri.FromRepository(source)
	if err != nil {
		return err
	}
	if b.Base.Manifest.Source != expected {
		return errors.New("runtime lexical source mismatch")
	}
	manifestID, err := b.Base.Manifest.ID()
	if err != nil {
		return err
	}
	if _, err := b.Base.Build.ID(); err != nil {
		return err
	}
	if b.Base.Build.ManifestID != manifestID {
		return errors.New("runtime lexical build scope mismatch")
	}
	count := 0
	for _, shard := range b.Base.Build.Shards {
		count += shard.Files
	}
	if count != len(b.Base.Manifest.Files) {
		return errors.New("runtime lexical build file coverage mismatch")
	}
	for _, path := range []string{b.Base.ManifestPath, b.Base.SourceRoot, b.Base.IndexPath, b.Executable} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("runtime lexical absolute normalized paths required")
		}
	}
	if err := safepath.RequireDigest(b.ExecutableSHA256); err != nil {
		return err
	}
	if b.Overlay != nil {
		if candidate == "" || b.Overlay.Candidate != candidate {
			return errors.New("runtime lexical candidate mismatch")
		}
		if _, err := b.Overlay.ID(b.Base.Manifest); err != nil {
			return err
		}
	}
	return nil
}

// ID seals the complete transport binding using the bounded canonical protocol.
func (b LexicalBinding) ID(source repository.Identity, candidate string) (string, error) {
	if err := b.Validate(source, candidate); err != nil {
		return "", err
	}
	return canonical.Hash("harness.runtime.lexical-binding.v1", b)
}

func lexicalContinuation(s State, binding *LexicalBinding) error {
	if s.LexicalRecord != nil {
		if binding == nil || s.Source == nil {
			return errors.New("compact continuation binding missing")
		}
		candidate, err := s.lexicalCandidate()
		if err != nil {
			return err
		}
		actual, err := binding.Record(*s.Source, candidate)
		if err != nil {
			return err
		}
		expectedID, err := canonical.Hash("harness.runtime.lexical-record.v1", *s.LexicalRecord)
		if err != nil {
			return err
		}
		actualID, err := canonical.Hash("harness.runtime.lexical-record.v1", actual)
		if err != nil {
			return err
		}
		if actualID != expectedID {
			return errors.New("compact continuation lexical binding changed")
		}
		return nil
	}
	if (s.Lexical == nil) != (binding == nil) {
		return errors.New("continuation lexical binding mismatch")
	}
	if binding == nil {
		return nil
	}
	if s.Source == nil {
		return errors.New("continuation lexical source missing")
	}
	candidate := ""
	if s.Candidate != nil {
		var err error
		candidate, err = s.Candidate.Candidate.ID()
		if err != nil {
			return err
		}
	}
	expected, err := s.Lexical.ID(*s.Source, candidate)
	if err != nil {
		return err
	}
	actual, err := binding.ID(*s.Source, candidate)
	if err != nil {
		return err
	}
	if actual != expected {
		return errors.New("continuation lexical binding changed")
	}
	return nil
}

// validateLexicalAdmission checks adapter metadata before any runtime intent is
// persisted. Actual artifacts are selected and read back by the controller.
func (a *Adapter) validateLexicalAdmission() error {
	if a.Lexical == nil {
		return nil
	}
	if a.Source == nil {
		return errors.New("runtime lexical source required")
	}
	candidate := ""
	if a.Candidate != nil {
		if err := a.Candidate.Validate(*a.Source); err != nil {
			return err
		}
		var err error
		candidate, err = a.Candidate.Candidate.ID()
		if err != nil {
			return err
		}
	}
	_, err := a.Lexical.Record(*a.Source, candidate)
	return err
}

// ValidateLexicalBinding checks an observed runtime's legacy or compact record
// against the controller-selected binding without filesystem access.
func (s State) ValidateLexicalBinding(binding *LexicalBinding) error {
	if s.Lexical != nil && s.LexicalRecord != nil {
		return errors.New("ambiguous runtime lexical evidence")
	}
	return lexicalContinuation(s, binding)
}

func (s State) lexicalCandidate() (string, error) {
	if s.Candidate == nil {
		return "", nil
	}
	return s.Candidate.Candidate.ID()
}
