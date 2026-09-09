package opencode

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"harness.local/engorch/internal/canonical"
)

// Client reads a controller-selected local OpenCode server. It grants no model
// dispatch authority. Loopback is a transport restriction, not OS isolation.
type Client struct {
	base, user, password string
	http                 *http.Client
	messageHTTP          *http.Client
	policy               TransportPolicy
}

// NewClient requires an explicit loopback IP/port and nonempty server credentials.
// DNS names, proxies, userinfo, alternate paths and redirects are unsupported.
func NewClient(endpoint, user, password string) (*Client, error) {
	return NewClientWithPolicy(endpoint, user, password, TransportPolicy{})
}

// NewClientWithPolicy retains NewClient's endpoint and credential restrictions
// while allowing bounded policy injection for runtime configuration and tests.
func NewClientWithPolicy(endpoint, user, password string, policy TransportPolicy) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.RawPath != "" {
		return nil, errors.New("invalid OpenCode endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	port, err := strconv.Atoi(u.Port())
	if ip == nil || !ip.IsLoopback() || err != nil || port < 1 || port > 65535 {
		return nil, errors.New("explicit loopback endpoint required")
	}
	if user == "" || len(user) > 128 || strings.ContainsAny(user, ":\r\n") || password == "" || len(password) > 4096 || strings.ContainsAny(password, "\r\n") {
		return nil, errors.New("invalid server credentials")
	}
	policy, err = normalizeTransportPolicy(policy)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: policy.ConnectTimeout}).DialContext, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: policy.ResponseHeaderTimeout, MaxResponseHeaderBytes: 64 << 10}
	messageTransport := &http.Transport{DialContext: (&net.Dialer{Timeout: policy.ConnectTimeout}).DialContext, IdleConnTimeout: 30 * time.Second, MaxResponseHeaderBytes: 64 << 10}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: endpoint, user: user, password: password, policy: policy, http: &http.Client{Transport: transport, CheckRedirect: noRedirect}, messageHTTP: &http.Client{Transport: messageTransport, CheckRedirect: noRedirect}}, nil
}

func validLoopbackEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.RawPath != "" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	port, err := strconv.Atoi(u.Port())
	return ip != nil && ip.IsLoopback() && err == nil && port >= 1 && port <= 65535
}

// Close releases idle connections without promising credential erasure.
func (c *Client) Close() {
	if c != nil && c.http != nil {
		c.http.CloseIdleConnections()
	}
	if c != nil && c.messageHTTP != nil {
		c.messageHTTP.CloseIdleConnections()
	}
}

func locator(s string) bool {
	if len(s) == 0 || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// ReadMessage retrieves one final message and validates its exact locator, binding
// and visible text. It does not establish session-wide quiescence or call a model.
func (c *Client) ReadMessage(ctx context.Context, b Binding, messageID string) (Assistant, string, error) {
	if c == nil || c.http == nil || !locator(b.SessionID) || !locator(messageID) {
		return Assistant{}, "", errors.New("invalid message locator")
	}
	raw, err := c.read(ctx, "/session/"+b.SessionID+"/message/"+messageID)
	if err != nil {
		return Assistant{}, "", err
	}
	m, err := wireObject(raw)
	if err != nil {
		return Assistant{}, "", err
	}
	a, err := DecodeAssistant(m["info"], b)
	if err != nil {
		return Assistant{}, "", err
	}
	if a.ID != messageID {
		return Assistant{}, "", errors.New("message response locator mismatch")
	}
	text, err := DecodeTextParts(m["parts"], a)
	if err != nil {
		return Assistant{}, "", err
	}
	return a, text, nil
}

func (c *Client) read(ctx context.Context, path string) ([]byte, error) {
	return c.request(ctx, http.MethodGet, path, nil)
}

func (c *Client) readConfiguration(ctx context.Context, workingDirectory string) ([]byte, error) {
	if !filepath.IsAbs(workingDirectory) || filepath.Clean(workingDirectory) != workingDirectory {
		return nil, errors.New("invalid OpenCode configuration directory")
	}
	query := url.Values{}
	query.Set("directory", workingDirectory)
	requestURL := url.URL{Path: "/config", RawQuery: query.Encode()}
	return c.read(ctx, requestURL.RequestURI())
}

func (c *Client) request(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	return c.requestStatus(ctx, method, path, body, http.StatusOK)
}

func (c *Client) requestStatus(ctx context.Context, method, path string, body []byte, status int) ([]byte, error) {
	if c == nil || c.http == nil || c.messageHTTP == nil {
		return nil, errors.New("uninitialized OpenCode client")
	}
	started := time.Now()
	phase, sessionID, requestMessageID := requestTransportIdentity(method, path, body)
	messagePost := phase == TransportFailurePhaseMessagePostWait
	if ctx == nil {
		return nil, errors.New("OpenCode request requires context")
	}
	if messagePost {
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("synchronous OpenCode message POST requires deadline")
		}
	} else {
		var cancel context.CancelFunc
		var err error
		ctx, cancel, err = boundedReadbackContext(ctx, c.policy.ReadbackTimeout)
		if err != nil {
			return nil, err
		}
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		evidence := c.transportFailureEvidence(err, ctx, phase, method, sessionID, requestMessageID, TransportDispatchStateNotDispatched, started)
		return nil, readbackFailure{message: "OpenCode readback unavailable", class: networkReadinessClass(ctx, err), cause: err, evidence: &evidence}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid readback request")
	}
	req.SetBasicAuth(c.user, c.password)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "engorch/1.0.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	httpClient := c.http
	if messagePost {
		httpClient = c.messageHTTP
	}
	res, err := httpClient.Do(req)
	if err != nil {
		evidence := c.transportFailureEvidence(err, req.Context(), phase, method, sessionID, requestMessageID, TransportDispatchStateUnknown, started)
		return nil, readbackFailure{message: "OpenCode readback unavailable", class: networkReadinessClass(ctx, err), cause: err, evidence: &evidence}
	}
	defer res.Body.Close()
	if status == http.StatusNoContent {
		if res.StatusCode != status {
			return nil, errors.New("OpenCode dispatch unresolved")
		}
		return nil, nil
	}
	kind, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if res.StatusCode != 200 {
		return nil, readbackFailure{message: "invalid OpenCode HTTP response", class: "http-status-" + strconv.Itoa(res.StatusCode)}
	}
	if err != nil || kind != "application/json" {
		return nil, readbackFailure{message: "invalid OpenCode HTTP response", class: "content-type"}
	}
	if res.ContentLength > 1<<20 {
		return nil, readbackFailure{message: "invalid OpenCode HTTP response", class: "content-length"}
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		evidence := c.transportFailureEvidence(err, req.Context(), phase, method, sessionID, requestMessageID, TransportDispatchStateUnknown, started)
		return nil, readbackFailure{message: "OpenCode response incomplete or oversized", class: "body-incomplete", cause: err, evidence: &evidence}
	}
	if len(raw) > 1<<20 {
		return nil, readbackFailure{message: "OpenCode response incomplete or oversized", class: "body-incomplete"}
	}
	return raw, nil
}

func (c *Client) transportFailureEvidence(err error, ctx context.Context, phase, method, sessionID, requestMessageID, dispatchState string, started time.Time) TransportFailureEvidence {
	contextError := ""
	if ctx != nil && ctx.Err() != nil {
		contextError = ctx.Err().Error()
	}
	return TransportFailureEvidence{
		Version: TransportFailureEvidenceVersion, TransportError: safeTransportError(err, c.user, c.password), ContextError: contextError,
		Phase: phase, ElapsedMillis: time.Since(started).Milliseconds(), Endpoint: c.base, HTTPMethod: method,
		SessionID: sessionID, RequestMessageID: requestMessageID, DispatchState: dispatchState,
	}
}

func (c *Client) diagnosticHealth(ctx context.Context) (bool, string) {
	var gotConnection, wroteRequest, gotResponse atomic.Bool
	trace := &httptrace.ClientTrace{
		GotConn:              func(httptrace.GotConnInfo) { gotConnection.Store(true) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { wroteRequest.Store(true) },
		GotFirstResponseByte: func() { gotResponse.Store(true) },
	}
	raw, err := c.read(httptrace.WithClientTrace(ctx, trace), "/global/health")
	if err == nil {
		var health struct {
			Healthy bool   `json:"healthy"`
			Version string `json:"version"`
		}
		if canonical.Decode(raw, &health) != nil || !health.Healthy || !safeHealthVersion(health.Version) {
			return true, "health-body"
		}
		return true, ""
	}
	class := readinessErrorClass(err)
	if strings.HasPrefix(class, "http-status-") || class == "content-type" || class == "content-length" || class == "body-incomplete" {
		return true, class
	}
	switch {
	case !gotConnection.Load():
		return gotResponse.Load(), "connect-" + class
	case !wroteRequest.Load():
		return gotResponse.Load(), "write-" + class
	default:
		return gotResponse.Load(), "read-" + class
	}
}

func safeHealthVersion(version string) bool {
	if len(version) < 1 || len(version) > 64 {
		return false
	}
	for _, character := range version {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '-' || character == '+') {
			return false
		}
	}
	return true
}

func networkReadinessClass(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "deadline"
		}
		return "canceled"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "connection-refused"
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return "connection-reset"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "network-timeout"
	}
	return "network-other"
}
