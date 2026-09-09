package gitpush

import (
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/worktree"
)

// Plan binds one committed candidate to one explicit destination and branch.
// ExpectedOld nil means the remote branch must be absent, not an unknown value.
type Plan struct {
	Version      int                `json:"version"`
	Nonce        string             `json:"nonce"`
	RepositoryID string             `json:"repository_id"`
	Workspace    worktree.Binding   `json:"workspace"`
	Candidate    worktree.Candidate `json:"candidate"`
	Destination  string             `json:"destination"`
	TargetRef    string             `json:"target_ref"`
	ExpectedOld  *string            `json:"expected_old"`
}

func validText(text string, limit int) bool {
	if text == "" || len(text) > limit || !utf8.ValidString(text) || strings.TrimSpace(text) != text {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validDestination(destination string) bool {
	if !validText(destination, 4096) {
		return false
	}
	if filepath.IsAbs(destination) {
		// Local destinations support isolated bare-repository qualification. UNC
		// paths are remote transport and are not treated as local destinations.
		return filepath.Clean(destination) == destination && !strings.HasPrefix(destination, `\\`) && !strings.HasPrefix(destination, "//")
	}
	u, err := url.Parse(destination)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && !u.ForceQuery && validText(u.Path, 4096) && u.Opaque == "" && u.String() == destination
}

func validTarget(ref string) bool {
	if !validText(ref, 1024) || !strings.HasPrefix(ref, "refs/heads/") || strings.ContainsAny(ref, " ~^:?*[\\") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.HasSuffix(ref, ".") {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// ValidateTargetRef accepts one explicit branch ref, never a tag or refspec.
func ValidateTargetRef(ref string) error {
	if !validTarget(ref) {
		return errors.New("invalid branch ref")
	}
	return nil
}

// ID checks exact publication inputs without reading Git, contacting a server or
// establishing that the candidate is committed, verified or approved for release.
func (p Plan) ID() (string, error) {
	if p.Version != 1 || !validText(p.Nonce, 128) || !validDestination(p.Destination) || !validTarget(p.TargetRef) {
		return "", errors.New("invalid push version, nonce, destination or branch")
	}
	if err := safepath.RequireDigest(p.RepositoryID); err != nil {
		return "", err
	}
	workspaceID, err := p.Workspace.ID()
	if err != nil {
		return "", err
	}
	if err := p.Candidate.ValidateBinding(p.Workspace); err != nil {
		return "", err
	}
	if p.Candidate.WorktreeID != workspaceID || p.Candidate.Head != p.Workspace.Request.Source.Commit {
		return "", errors.New("push candidate and committed workspace differ")
	}
	if p.ExpectedOld != nil && (len(*p.ExpectedOld) != len(p.Candidate.Head) || strings.Trim(*p.ExpectedOld, "0123456789abcdef") != "" || strings.Trim(*p.ExpectedOld, "0") == "") {
		return "", errors.New("invalid expected remote commit")
	}
	return canonical.Hash("harness.git-push-plan.v1", p)
}

// Intent binds the validated push payload to the run's approved implementation plan.
func (p Plan) Intent(planID string) (effects.Intent, error) {
	id, err := p.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	i := effects.Intent{Version: 1, RunID: p.Workspace.Request.RunID, PlanID: planID, RepositoryID: p.RepositoryID, Kind: "push", InputHash: id}
	if _, err := i.ID(); err != nil {
		return effects.Intent{}, err
	}
	return i, nil
}
