package draftpr

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitpush"
)

const markerPrefix = "<!-- engorch-draft-intent:"

// Plan binds draft text and base branch to the exact previously proposed push.
// A controller must additionally establish that the push was confirmed.
type Plan struct {
	Version    int            `json:"version"`
	Nonce      string         `json:"nonce"`
	Push       gitpush.Plan   `json:"push"`
	PushIntent effects.Intent `json:"push_intent"`
	Repository string         `json:"repository"`
	BaseRef    string         `json:"base_ref"`
	BaseCommit string         `json:"base_commit"`
	Title      string         `json:"title"`
	Body       string         `json:"body"`
}

func label(text string, limit int) bool {
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

func repositoryName(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > 100 {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}

// ID validates a same-repository GitHub draft payload and computes its identity.
// It does not claim that branches exist, a push succeeded or GitHub supports the
// repository's object format. Credentials are never part of this payload.
func (p Plan) ID() (string, error) {
	if p.Version != 1 || !label(p.Nonce, 128) || !repositoryName(p.Repository) || !label(p.Title, 256) || len(p.Body) > 32768 || !utf8.ValidString(p.Body) || strings.ContainsAny(p.Body, "\x00\r") || strings.Contains(strings.ToLower(p.Body), markerPrefix) {
		return "", errors.New("invalid draft identity, repository or text")
	}
	if err := gitpush.ValidateTargetRef(p.BaseRef); err != nil {
		return "", err
	}
	if p.BaseRef == p.Push.TargetRef {
		return "", errors.New("draft base and head must differ")
	}
	if len(p.BaseCommit) != len(p.Push.Candidate.Head) || strings.Trim(p.BaseCommit, "0123456789abcdef") != "" || strings.Trim(p.BaseCommit, "0") == "" {
		return "", errors.New("invalid expected draft base commit")
	}
	intent, err := p.Push.Intent(p.PushIntent.PlanID)
	if err != nil {
		return "", err
	}
	if intent != p.PushIntent {
		return "", errors.New("draft push evidence substituted")
	}
	url := "https://github.com/" + p.Repository
	if p.Push.Destination != url && p.Push.Destination != url+".git" {
		return "", errors.New("draft repository differs from push destination")
	}
	return canonical.Hash("harness.draft-pr-plan.v1", p)
}

// Intent binds draft creation to the same run, plan and repository as its push.
func (p Plan) Intent() (effects.Intent, error) {
	id, err := p.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	intent := p.PushIntent
	intent.Kind, intent.InputHash = "draft_pr", id
	if _, err := intent.ID(); err != nil {
		return effects.Intent{}, err
	}
	return intent, nil
}

// CreateRequest is the exact request body; it always creates a draft and does
// not delegate head modification to maintainers through this request.
type CreateRequest struct {
	Title               string `json:"title"`
	Body                string `json:"body"`
	Head                string `json:"head"`
	Base                string `json:"base"`
	Draft               bool   `json:"draft"`
	MaintainerCanModify bool   `json:"maintainer_can_modify"`
}

// Request derives a deterministic correlation marker from the full effect ID.
// That marker assists reconciliation but is not hosted proof or idempotency.
func (p Plan) Request() (CreateRequest, error) {
	intent, err := p.Intent()
	if err != nil {
		return CreateRequest{}, err
	}
	id, err := intent.ID()
	if err != nil {
		return CreateRequest{}, err
	}
	return CreateRequest{Title: p.Title, Body: p.Body + "\n\n" + markerPrefix + id + " -->", Head: strings.TrimPrefix(p.Push.TargetRef, "refs/heads/"), Base: strings.TrimPrefix(p.BaseRef, "refs/heads/"), Draft: true, MaintainerCanModify: false}, nil
}
