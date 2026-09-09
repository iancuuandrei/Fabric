package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// HostConfiguration is evidence of one bounded /config response. Its digest
// binds the exact wire bytes, including unknown fields. This does not establish
// process identity, absence of internal plugins/network activity, or a sandbox.
type HostConfiguration struct {
	SHA256 string `json:"sha256"`
}

// ReadHostConfiguration admits the required bootstrap configuration projection.
// The caller separately owns and identifies the process behind the endpoint.
func (c *Client) ReadHostConfiguration(ctx context.Context) (HostConfiguration, error) {
	raw, err := c.read(ctx, "/config")
	if err != nil {
		return HostConfiguration{}, err
	}
	return decodeHostConfiguration(raw)
}

func decodeHostConfiguration(raw []byte) (HostConfiguration, error) {
	config, err := wireObject(raw)
	if err != nil {
		return HostConfiguration{}, err
	}
	var share string
	if field(config, "share", &share) != nil || share != "disabled" {
		return HostConfiguration{}, errors.New("OpenCode sharing is not disabled")
	}
	var plugins []json.RawMessage
	if json.Unmarshal(config["plugin"], &plugins) != nil || plugins == nil || len(plugins) != 0 {
		return HostConfiguration{}, errors.New("OpenCode configured plugins are not empty")
	}
	var permissions map[string]string
	if field(config, "permission", &permissions) != nil || len(permissions) != 1 || permissions["*"] != "deny" {
		return HostConfiguration{}, errors.New("OpenCode permission ceiling mismatch")
	}
	hash := sha256.Sum256(raw)
	return HostConfiguration{SHA256: hex.EncodeToString(hash[:])}, nil
}
