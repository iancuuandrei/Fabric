package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
)

// ReconcileSession searches the exact creation title marker then independently
// reads one matching session. Zero or multiple candidates remain unresolved;
// absence is never proof that creation did not happen. The bounded search does
// not follow links or create sessions and assumes an admitted local host context.
// Both marker search and locator readback select the bound directory explicitly.
func (c *Client) ReconcileSession(ctx context.Context, b SessionBinding) (string, error) {
	return c.reconcileSession(ctx, b, denySessionPermissions, "")
}

func (c *Client) reconcileSession(ctx context.Context, b SessionBinding, permissions []sessionPermissionRule, catalogSHA256 string) (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	query := url.Values{"search": {"engorch:" + b.IntentID}, "limit": {"2"}, "scope": {"project"}, "directory": {b.Directory}}
	raw, err := c.read(ctx, "/session?"+query.Encode())
	if err != nil {
		return "", err
	}
	if len(raw) > (1<<20)-10 {
		return "", errors.New("session list exceeds bound")
	}
	wrapped := append([]byte(`{"items":`), raw...)
	wrapped = append(wrapped, '}')
	m, err := wireObject(wrapped)
	if err != nil {
		return "", err
	}
	var rows []json.RawMessage
	if json.Unmarshal(m["items"], &rows) != nil || len(rows) != 1 {
		return "", errors.New("session creation unresolved or ambiguous")
	}
	id, err := DecodeSession(rows[0], b)
	if err != nil {
		return "", err
	}
	raw, err = c.read(ctx, "/session/"+id+"?"+url.Values{"directory": {b.Directory}}.Encode())
	if err != nil {
		return "", err
	}
	actual, err := DecodeSession(raw, b)
	if err != nil {
		return "", err
	}
	if actual != id {
		return "", errors.New("session readback identity changed")
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
