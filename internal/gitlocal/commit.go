package gitlocal

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/worktree"
)

// Identity fixes Git attribution and time without reading ambient user settings.
// Times are Unix seconds rendered in UTC by the eventual local object executor.
type Identity struct {
	Name        string `json:"name"`
	Email       string `json:"email"`
	UnixSeconds int64  `json:"unix_seconds"`
}

// Validate rejects missing attribution and Git header delimiters/control bytes.
func (i Identity) Validate() error {
	for _, value := range []string{i.Name, i.Email} {
		if strings.TrimSpace(value) != value || value == "" || len(value) > 256 || !utf8.ValidString(value) || strings.ContainsAny(value, "<>") {
			return errors.New("invalid commit identity")
		}
		for _, r := range value {
			if unicode.IsControl(r) {
				return errors.New("control character in commit identity")
			}
		}
	}
	if strings.ContainsAny(i.Email, " \t") || !strings.Contains(i.Email, "@") || i.UnixSeconds < 0 || i.UnixSeconds > 253402300799 {
		return errors.New("invalid commit email or timestamp")
	}
	return nil
}

// CommitPlan binds an entire candidate manifest and exact attribution/message.
// Its single parent and target branch come from the admitted workspace binding.
type CommitPlan struct {
	Version   int                  `json:"version"`
	Nonce     string               `json:"nonce"`
	Workspace worktree.Binding     `json:"workspace"`
	Candidate worktree.Candidate   `json:"candidate"`
	Files     []worktree.FileState `json:"files"`
	Author    Identity             `json:"author"`
	Committer Identity             `json:"committer"`
	Message   string               `json:"message"`
}

// ID validates frozen inputs and computes a domain-separated approval identity.
// It does not assert current filesystem freshness, readiness or object existence.
func (p CommitPlan) ID() (string, error) {
	if p.Version != 1 || strings.TrimSpace(p.Nonce) == "" || len(p.Nonce) > 128 {
		return "", errors.New("invalid commit plan version or nonce")
	}
	id, err := p.Workspace.ID()
	if err != nil {
		return "", err
	}
	if err := p.Candidate.ValidateBinding(p.Workspace); err != nil {
		return "", err
	}
	if p.Candidate.WorktreeID != id || p.Candidate.Head != p.Workspace.Request.Source.Commit {
		return "", errors.New("commit candidate/workspace mismatch")
	}
	hash, err := worktree.FilesID(p.Files)
	if err != nil {
		return "", err
	}
	if p.Files == nil || hash != p.Candidate.FilesHash || len(p.Files) != p.Candidate.FileCount {
		return "", errors.New("commit manifest mismatch")
	}
	if err := p.Author.Validate(); err != nil {
		return "", err
	}
	if err := p.Committer.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Message) == "" || len(p.Message) > 64<<10 || !utf8.ValidString(p.Message) || strings.ContainsAny(p.Message, "\x00\r") || !strings.HasSuffix(p.Message, "\n") {
		return "", errors.New("commit message must be nonempty bounded UTF-8 with LF and a final newline")
	}
	return canonical.Hash("harness.git-commit-plan.v1", p)
}
