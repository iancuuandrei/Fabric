package codexrpc

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
)

// Server identifies observed server metadata, without claiming model access.
type Server struct {
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
	UserAgent      string `json:"userAgent"`
}

// Initialize completes the installed v1 handshake and verifies the isolated home.
// The caller must launch the process with that home; this method does not set it.
func (c *Client) Initialize(ctx context.Context, expectedHome string) (Server, error) {
	return c.InitializeCapabilities(ctx, expectedHome, false)
}

// InitializeCapabilities explicitly opts into version-specific experimental
// fields when required by an admitted host policy, including empty environments.
func (c *Client) InitializeCapabilities(ctx context.Context, expectedHome string, experimental bool) (Server, error) {
	var server Server
	response, events, err := c.Call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "engorch", "version": "1.0.0-dev"},
		"capabilities": map[string]any{"experimentalApi": experimental},
	})
	if err != nil {
		return server, err
	}
	if len(response.Error) != 0 || len(events) != 0 {
		_ = c.Close()
		return server, errors.New("provider initialization rejected or emitted unexpected events")
	}
	if err := json.Unmarshal(response.Result, &server); err != nil {
		_ = c.Close()
		return server, err
	}
	if !filepath.IsAbs(expectedHome) || filepath.Clean(server.CodexHome) != filepath.Clean(expectedHome) || server.PlatformFamily == "" || server.PlatformOS == "" || server.UserAgent == "" {
		_ = c.Close()
		return server, errors.New("provider handshake metadata or home mismatch")
	}
	if err := c.Notify(ctx, "initialized", map[string]any{}); err != nil {
		return server, err
	}
	return server, nil
}
