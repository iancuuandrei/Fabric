package opencode

import (
	"errors"
	"strings"
	"unicode/utf8"

	"harness.local/engorch/internal/safepath"
)

// SessionBinding is the controller's expected creation marker and host context.
// A marker correlates observations; it is not an idempotency or ownership token.
type SessionBinding struct {
	IntentID  string `json:"intent_id"`
	ProjectID string `json:"project_id"`
	Directory string `json:"directory"`
	Agent     string `json:"agent"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Variant   string `json:"variant"`
}

// DecodeSession validates an unshared root session against a durable creation
// marker. Tool permissions and host ownership require separate admission before
// any inference; this projection is not permission to dispatch.
func DecodeSession(raw []byte, expected SessionBinding) (string, error) {
	if err := expected.Validate(); err != nil {
		return "", err
	}
	m, err := wireObject(raw)
	if err != nil {
		return "", err
	}
	for _, key := range []string{"share", "parentID", "revert"} {
		if _, ok := m[key]; ok {
			return "", errors.New("session is shared, forked or reverted")
		}
	}
	for key, want := range map[string]string{"projectID": expected.ProjectID, "directory": expected.Directory, "agent": expected.Agent, "title": "engorch:" + expected.IntentID} {
		var got string
		if field(m, key, &got) != nil || got != want {
			return "", errors.New("session context mismatch")
		}
	}
	metadata, err := wireObject(m["metadata"])
	if err != nil {
		return "", errors.New("session marker missing")
	}
	var marker string
	if field(metadata, "engorch_intent_id", &marker) != nil || marker != expected.IntentID {
		return "", errors.New("session marker mismatch")
	}
	var model struct {
		ID       string `json:"id"`
		Provider string `json:"providerID"`
		Variant  string `json:"variant,omitempty"`
	}
	if field(m, "model", &model) != nil || model.ID != expected.Model || model.Provider != expected.Provider || model.Variant != expected.Variant {
		return "", errors.New("session model substitution")
	}
	var id string
	if field(m, "id", &id) != nil || !locator(id) {
		return "", errors.New("invalid session ID")
	}
	return id, nil
}

// Validate rejects invalid expected context before any remote session operation.
func (b SessionBinding) Validate() error {
	if safepath.RequireDigest(b.IntentID) != nil {
		return errors.New("invalid session intent identity")
	}
	for _, value := range []string{b.ProjectID, b.Directory, b.Agent, b.Provider, b.Model} {
		if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || len(value) > 4096 {
			return errors.New("invalid expected session context")
		}
	}
	if !utf8.ValidString(b.Variant) || len(b.Variant) > 4096 {
		return errors.New("invalid expected session variant")
	}
	return nil
}
