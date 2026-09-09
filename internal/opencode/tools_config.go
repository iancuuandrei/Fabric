package opencode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
)

const (
	// ToolsMCPServerName is the sole pinned OpenCode MCP server identity.
	ToolsMCPServerName = "engorch"
	// MaxToolsMCPNames bounds the exact tool-name capability set.
	MaxToolsMCPNames = 128
	toolsMCPServer   = ToolsMCPServerName
)

// ToolsConfigurationSpec is the controller-owned capability granted to one
// OpenCode process. Bearer is admission input and is never copied to a receipt.
type ToolsConfigurationSpec struct {
	Endpoint      string
	Bearer        string
	ToolNames     []string
	TimeoutMillis int
	// AllowStructuredOutput is an explicit native writer/fixer opt-in. It
	// grants only the stock terminal permission; it does not alter ToolNames.
	AllowStructuredOutput bool
}

// ToolsConfigurationReceipt binds one admitted /config response without
// exposing its invocation bearer. ToolIDs are the names OpenCode presents to
// the model and uses as permission keys; the optional native terminal marker
// is recorded separately from that controller catalog.
type ToolsConfigurationReceipt struct {
	SHA256                string   `json:"sha256"`
	MCPServer             string   `json:"mcp_server"`
	Endpoint              string   `json:"endpoint"`
	ToolIDs               []string `json:"tool_ids"`
	TimeoutMillis         int      `json:"timeout_millis"`
	AllowStructuredOutput bool     `json:"allow_structured_output,omitempty"`
}

type normalizedToolsConfiguration struct {
	endpoint              string
	bearer                string
	toolIDs               []string
	timeoutMillis         int
	allowStructuredOutput bool
}

// BuildToolsConfiguration returns the complete compact
// OPENCODE_CONFIG_CONTENT for one controller-owned MCP endpoint.
func BuildToolsConfiguration(spec ToolsConfigurationSpec) (string, error) {
	expected, err := normalizeToolsConfiguration(spec)
	if err != nil {
		return "", err
	}
	permission := bytes.NewBufferString(`{"*":"deny"`)
	for _, id := range expected.toolIDs {
		encoded, _ := json.Marshal(id)
		permission.WriteByte(',')
		permission.Write(encoded)
		permission.WriteString(`:"allow"`)
	}
	if expected.allowStructuredOutput {
		permission.WriteString(`,"` + StructuredOutputToolName + `":"allow"`)
	}
	permission.WriteByte('}')
	payload := struct {
		Share      string                    `json:"share"`
		Plugin     []string                  `json:"plugin"`
		MCP        map[string]toolsMCPConfig `json:"mcp"`
		Permission json.RawMessage           `json:"permission"`
	}{
		Share:  "disabled",
		Plugin: []string{},
		MCP: map[string]toolsMCPConfig{ToolsMCPServerName: {
			Type: "remote", URL: expected.endpoint, Enabled: true,
			Headers: map[string]string{"Authorization": "Bearer " + expected.bearer},
			OAuth:   false, Timeout: expected.timeoutMillis,
		}},
		Permission: permission.Bytes(),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", errors.New("cannot encode OpenCode tools configuration")
	}
	return string(raw), nil
}

type toolsMCPConfig struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Enabled bool              `json:"enabled"`
	Headers map[string]string `json:"headers"`
	OAuth   bool              `json:"oauth"`
	Timeout int               `json:"timeout"`
}

// ReadToolsConfiguration admits the exact static MCP and permission projection.
// The caller separately owns the endpoint process and MCP listener identities.
func (c *Client) ReadToolsConfiguration(ctx context.Context, expected ToolsConfigurationSpec) (ToolsConfigurationReceipt, error) {
	raw, err := c.read(ctx, "/config")
	if err != nil {
		return ToolsConfigurationReceipt{}, err
	}
	return decodeToolsConfiguration(raw, expected)
}

// ReadToolsConfigurationInDirectory admits the exact static MCP and
// permission projection from the selected workspace instance.
func (c *Client) ReadToolsConfigurationInDirectory(ctx context.Context, directory string, expected ToolsConfigurationSpec) (ToolsConfigurationReceipt, error) {
	raw, err := c.readConfiguration(ctx, directory)
	if err != nil {
		return ToolsConfigurationReceipt{}, err
	}
	return decodeToolsConfiguration(raw, expected)
}

func decodeToolsConfiguration(raw []byte, spec ToolsConfigurationSpec) (ToolsConfigurationReceipt, error) {
	expected, err := normalizeToolsConfiguration(spec)
	if err != nil {
		return ToolsConfigurationReceipt{}, err
	}
	config, err := wireObject(raw)
	if err != nil {
		return ToolsConfigurationReceipt{}, err
	}
	var share string
	if field(config, "share", &share) != nil || share != "disabled" {
		return ToolsConfigurationReceipt{}, errors.New("OpenCode sharing is not disabled")
	}
	var plugins []json.RawMessage
	if json.Unmarshal(config["plugin"], &plugins) != nil || plugins == nil || len(plugins) != 0 {
		return ToolsConfigurationReceipt{}, errors.New("OpenCode configured plugins are not empty")
	}
	var servers map[string]json.RawMessage
	if field(config, "mcp", &servers) != nil || len(servers) != 1 || servers[ToolsMCPServerName] == nil {
		return ToolsConfigurationReceipt{}, errors.New("OpenCode MCP server set mismatch")
	}
	server, err := wireObject(servers[ToolsMCPServerName])
	if err != nil || !toolsExactKeys(server, "type", "url", "enabled", "headers", "oauth", "timeout") {
		return ToolsConfigurationReceipt{}, errors.New("OpenCode MCP projection mismatch")
	}
	var kind, endpoint string
	var enabled, oauth bool
	var timeout int
	if field(server, "type", &kind) != nil || kind != "remote" ||
		field(server, "url", &endpoint) != nil || endpoint != expected.endpoint ||
		field(server, "enabled", &enabled) != nil || !enabled ||
		field(server, "oauth", &oauth) != nil || oauth ||
		field(server, "timeout", &timeout) != nil || timeout != expected.timeoutMillis {
		return ToolsConfigurationReceipt{}, errors.New("OpenCode MCP projection mismatch")
	}
	var headers map[string]string
	if field(server, "headers", &headers) != nil || len(headers) != 1 {
		return ToolsConfigurationReceipt{}, errors.New("OpenCode MCP headers mismatch")
	}
	wantAuthorization := []byte("Bearer " + expected.bearer)
	gotAuthorization := []byte(headers["Authorization"])
	if len(gotAuthorization) != len(wantAuthorization) || subtle.ConstantTimeCompare(gotAuthorization, wantAuthorization) != 1 {
		return ToolsConfigurationReceipt{}, errors.New("OpenCode MCP credential mismatch")
	}
	rules, err := orderedJSONObject(config["permission"])
	wantRuleCount := len(expected.toolIDs) + 1
	if expected.allowStructuredOutput {
		wantRuleCount++
	}
	if err != nil || len(rules) != wantRuleCount || rules[0].name != "*" || string(rules[0].value) != `"deny"` {
		return ToolsConfigurationReceipt{}, errors.New("OpenCode tools permission ceiling mismatch")
	}
	for i, id := range expected.toolIDs {
		if rules[i+1].name != id || string(rules[i+1].value) != `"allow"` {
			return ToolsConfigurationReceipt{}, errors.New("OpenCode tools permission order mismatch")
		}
	}
	if expected.allowStructuredOutput {
		last := rules[len(rules)-1]
		if last.name != StructuredOutputToolName || string(last.value) != `"allow"` {
			return ToolsConfigurationReceipt{}, errors.New("OpenCode structured output permission mismatch")
		}
	}
	hash := sha256.Sum256(raw)
	return ToolsConfigurationReceipt{
		SHA256: hex.EncodeToString(hash[:]), MCPServer: ToolsMCPServerName,
		Endpoint: expected.endpoint, ToolIDs: append([]string(nil), expected.toolIDs...),
		TimeoutMillis: expected.timeoutMillis, AllowStructuredOutput: expected.allowStructuredOutput,
	}, nil
}

type orderedJSONField struct {
	name  string
	value json.RawMessage
}

func orderedJSONObject(raw []byte) ([]orderedJSONField, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') {
		return nil, errors.New("ordered JSON object required")
	}
	var fields []orderedJSONField
	for decoder.More() {
		name, err := decoder.Token()
		if err != nil {
			return nil, errors.New("invalid ordered JSON object")
		}
		key, ok := name.(string)
		if !ok {
			return nil, errors.New("invalid ordered JSON key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("invalid ordered JSON value")
		}
		fields = append(fields, orderedJSONField{name: key, value: value})
	}
	close, err := decoder.Token()
	if err != nil || close != json.Delim('}') || decoder.More() {
		return nil, errors.New("invalid ordered JSON object")
	}
	return fields, nil
}

func normalizeToolsConfiguration(spec ToolsConfigurationSpec) (normalizedToolsConfiguration, error) {
	parsed, err := url.Parse(spec.Endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.Opaque != "" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Path != "/mcp" || parsed.RawPath != "" || parsed.String() != spec.Endpoint {
		return normalizedToolsConfiguration{}, errors.New("exact IPv4 loopback MCP endpoint required")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || parsed.Host != "127.0.0.1:"+strconv.Itoa(port) {
		return normalizedToolsConfiguration{}, errors.New("exact IPv4 loopback MCP endpoint required")
	}
	if len(spec.Bearer) < 32 || len(spec.Bearer) > 512 || !safeCapability(spec.Bearer) {
		return normalizedToolsConfiguration{}, errors.New("safe invocation bearer required")
	}
	if spec.TimeoutMillis < 1 || spec.TimeoutMillis > 30_000 {
		return normalizedToolsConfiguration{}, errors.New("bounded MCP timeout required")
	}
	ids, err := ToolPermissionIDs(spec.ToolNames)
	if err != nil {
		return normalizedToolsConfiguration{}, err
	}
	return normalizedToolsConfiguration{endpoint: spec.Endpoint, bearer: spec.Bearer, toolIDs: ids, timeoutMillis: spec.TimeoutMillis, allowStructuredOutput: spec.AllowStructuredOutput}, nil
}

// ToolPermissionIDs returns the sorted OpenCode permission keys for one MCP
// catalog. Session permissions use this to stay aligned with host configuration.
func ToolPermissionIDs(toolNames []string) ([]string, error) {
	if len(toolNames) == 0 || len(toolNames) > MaxToolsMCPNames {
		return nil, errors.New("bounded MCP tool catalog required")
	}
	names := append([]string(nil), toolNames...)
	sort.Strings(names)
	ids := make([]string, len(names))
	seenNames, seenIDs := map[string]bool{}, map[string]bool{}
	for i, name := range names {
		if len(name) == 0 || len(name) > 128 || !safeToolName(name) || seenNames[name] {
			return nil, errors.New("unique safe MCP tool names required")
		}
		id := ToolsMCPServerName + "_" + name
		if seenIDs[id] {
			return nil, errors.New("colliding OpenCode tool IDs")
		}
		seenNames[name], seenIDs[id], ids[i] = true, true, id
	}
	return ids, nil
}

func safeToolName(value string) bool {
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return value != ""
}

func safeCapability(value string) bool {
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return value != ""
}

func toolsExactKeys(object map[string]json.RawMessage, keys ...string) bool {
	if len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}
