package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
)

// MCPStatus binds one status readback. Connected status does not prove catalog
// identity or execution; those require observations at the owned MCP listener.
type MCPStatus struct {
	SHA256 string `json:"sha256"`
}

// ReadMCPStatus requires the sole controller-configured server to be connected.
// Callers should repeat this gate after a turn to detect dynamic server changes.
func (c *Client) ReadMCPStatus(ctx context.Context) (MCPStatus, error) {
	raw, err := c.read(ctx, "/mcp")
	if err != nil {
		return MCPStatus{}, err
	}
	return decodeMCPStatus(raw)
}

// ReadMCPStatusInDirectory requires the sole controller-configured server to
// be connected in the exact workspace instance selected by directory. Runtime
// startup should use this form so an ambient process directory cannot select a
// different cached instance before project admission.
func (c *Client) ReadMCPStatusInDirectory(ctx context.Context, directory string) (MCPStatus, error) {
	if validateProjectPath(directory) != nil {
		return MCPStatus{}, errors.New("invalid OpenCode MCP status directory")
	}
	query := url.Values{"directory": []string{directory}}
	raw, err := c.read(ctx, "/mcp?"+query.Encode())
	if err != nil {
		return MCPStatus{}, err
	}
	return decodeMCPStatus(raw)
}

func decodeMCPStatus(raw []byte) (MCPStatus, error) {
	servers, err := wireObject(raw)
	if err != nil || len(servers) != 1 {
		return MCPStatus{}, errors.New("OpenCode MCP server set mismatch")
	}
	server, err := wireObject(servers["engorch"])
	if err != nil || len(server) != 1 {
		return MCPStatus{}, errors.New("OpenCode MCP status shape mismatch")
	}
	var status string
	if field(server, "status", &status) != nil || status != "connected" {
		return MCPStatus{}, errors.New("OpenCode MCP server is not connected")
	}
	hash := sha256.Sum256(raw)
	return MCPStatus{SHA256: hex.EncodeToString(hash[:])}, nil
}
