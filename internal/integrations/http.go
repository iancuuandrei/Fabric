package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	maxHTTPResponseBytes = 2 << 20 // 2 MiB bounded read
	defaultHTTPTimeout   = 20 * time.Second
)

var (
	issueKeyRe    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[0-9]+$`)
	digitsOnlyRe  = regexp.MustCompile(`^[0-9]+$`)
	defaultClient = &http.Client{Timeout: defaultHTTPTimeout}
)

func httpClientOrDefault(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return defaultClient
}

// doJSON performs a bounded HTTP JSON request against an SSRF-validated endpoint.
func doJSON(ctx context.Context, client *http.Client, method, endpoint string, headers map[string]string, payload any, dst any) (int, []byte, error) {
	if err := ValidateEndpointURL(endpoint); err != nil {
		return 0, nil, err
	}
	var bodyReader io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClientOrDefault(client).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseBytes))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && dst != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, dst); err != nil {
			return resp.StatusCode, raw, fmt.Errorf("decode response JSON: %w", err)
		}
	}
	return resp.StatusCode, raw, nil
}

// ValidateTicketRef rejects path-traversal segments, whitespace, leading hyphens,
// and control characters from external ticket identifiers before URL interpolation.
func ValidateTicketRef(ref string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	trimmed = strings.TrimPrefix(trimmed, "#")
	if trimmed == "" || strings.HasPrefix(trimmed, "-") || strings.Contains(trimmed, "..") {
		return "", fmt.Errorf("invalid ticket reference %q", ref)
	}
	for _, r := range trimmed {
		if r < 0x20 || r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '\\' || r == '/' {
			return "", fmt.Errorf("invalid characters in ticket reference %q", ref)
		}
	}
	return trimmed, nil
}

// ParseReference classifies a ticket URL or bare issue identifier into
// (provider, key, optionalProject). When provider is ambiguous (e.g., "PAY-42"
// without a URL), provider is returned as "" so ResolveTracker picks the active tracker.
func ParseReference(raw string) (provider, key, project string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", ""
	}
	if strings.Contains(trimmed, "://") {
		u, err := url.Parse(trimmed)
		if err != nil {
			return "", "", ""
		}
		host := strings.ToLower(u.Hostname())
		segments := splitPath(u.Path)
		if idx := indexOf(segments, "browse"); idx >= 0 && idx+1 < len(segments) {
			return "jira", segments[idx+1], ""
		}
		if strings.Contains(host, "linear.app") {
			if idx := indexOf(segments, "issue"); idx >= 0 && idx+1 < len(segments) {
				return "linear", segments[idx+1], ""
			}
		}
		if idx := indexOf(segments, "-"); idx > 0 && idx+2 < len(segments) && segments[idx+1] == "issues" {
			return "gitlab", segments[idx+2], strings.Join(segments[:idx], "/")
		}
		if strings.Contains(host, "github.com") && len(segments) >= 4 && segments[2] == "issues" {
			return "github", strings.TrimPrefix(segments[3], "#"), ""
		}
	}
	if strings.HasPrefix(trimmed, "#") {
		num := strings.TrimPrefix(trimmed, "#")
		if digitsOnlyRe.MatchString(num) {
			return "github", num, ""
		}
	}
	if digitsOnlyRe.MatchString(trimmed) {
		return "", trimmed, ""
	}
	if issueKeyRe.MatchString(trimmed) {
		return "", strings.ToUpper(trimmed), ""
	}
	return "", trimmed, ""
}

func splitPath(p string) []string {
	var out []string
	for _, s := range strings.Split(strings.Trim(p, "/"), "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func indexOf(list []string, target string) int {
	for i, s := range list {
		if s == target {
			return i
		}
	}
	return -1
}

func httpError(service string, status int, raw []byte) error {
	msg := strings.TrimSpace(string(raw))
	if len(msg) > 256 {
		msg = msg[:256] + "..."
	}
	if msg == "" {
		return fmt.Errorf("%s HTTP %d", service, status)
	}
	return fmt.Errorf("%s HTTP %d: %s", service, status, msg)
}

func requireEnv(envName, label string) (string, error) {
	val := strings.TrimSpace(os.Getenv(envName))
	if val == "" {
		return "", fmt.Errorf("%s environment variable %s is unset or empty", label, envName)
	}
	return val, nil
}

// errString is returned cleanly on GraphQL error lists.
func joinGraphQLErrors(errs []struct {
	Message string `json:"message"`
}) error {
	if len(errs) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		if strings.TrimSpace(e.Message) != "" {
			msgs = append(msgs, e.Message)
		}
	}
	if len(msgs) == 0 {
		return errors.New("GraphQL error")
	}
	return errors.New(strings.Join(msgs, "; "))
}
