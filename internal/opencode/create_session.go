package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

type sessionPermissionRule struct {
	Permission string `json:"permission"`
	Pattern    string `json:"pattern"`
	Action     string `json:"action"`
}

var denySessionPermissions = []sessionPermissionRule{{Permission: "*", Pattern: "*", Action: "deny"}}

// CreateSessionOnce is a transport primitive for an already persisted creation
// intent on an owned, isolated host. It sends one POST, then independently reads
// the returned locator. Any failure is unresolved: callers must reconcile the
// marker, never repeat creation automatically. It does not invoke a model.
// Creation and independent readback both select the exact bound directory.
func (c *Client) CreateSessionOnce(ctx context.Context, b SessionBinding) (string, error) {
	return c.createSessionOnce(ctx, b, denySessionPermissions, "")
}

func (c *Client) createSessionOnce(ctx context.Context, b SessionBinding, permissions []sessionPermissionRule, catalogSHA256 string) (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	metadata := map[string]string{"engorch_intent_id": b.IntentID}
	if catalogSHA256 != "" {
		metadata["engorch_catalog_sha256"] = catalogSHA256
	}
	body, err := json.Marshal(map[string]any{
		"title": "engorch:" + b.IntentID, "agent": b.Agent,
		"metadata":   metadata,
		"model":      map[string]string{"id": b.Model, "providerID": b.Provider, "variant": b.Variant},
		"permission": permissions,
	})
	if err != nil {
		return "", errors.New("invalid session request")
	}
	directoryQuery := url.Values{"directory": {b.Directory}}.Encode()
	raw, err := c.request(ctx, http.MethodPost, "/session?"+directoryQuery, body)
	if err != nil {
		return "", err
	}
	id, err := DecodeSession(raw, b)
	if err != nil {
		return "", err
	}
	raw, err = c.read(ctx, "/session/"+id+"?"+directoryQuery)
	if err != nil {
		return "", err
	}
	actual, err := DecodeSession(raw, b)
	if err != nil || actual != id {
		return "", errors.New("created session readback mismatch")
	}
	if err := validateSessionPermissions(raw, permissions); err != nil {
		return "", err
	}
	if catalogSHA256 != "" {
		if err := validateToolSessionMetadata(raw, b.IntentID, catalogSHA256); err != nil {
			return "", err
		}
	}
	return id, nil
}

// validateSessionPermissions applies equally to creation and recovery readback.
func validateSessionPermissions(raw []byte, expected []sessionPermissionRule) error {
	m, err := wireObject(raw)
	if err != nil {
		return err
	}
	var rules []sessionPermissionRule
	if field(m, "permission", &rules) != nil || len(rules) != len(expected) {
		return errors.New("session permission mismatch")
	}
	for n := range expected {
		if rules[n] != expected[n] {
			return errors.New("session permission mismatch")
		}
	}
	return nil
}
