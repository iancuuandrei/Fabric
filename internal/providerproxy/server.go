package providerproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/providercredential"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/providertransport"
	"harness.local/engorch/internal/safepath"
)

const (
	defaultHandlerTimeout = 5 * time.Minute
	maximumHandlerTimeout = 30 * time.Minute
	maximumHeaderBytes    = 32 << 10
)

// Config fixes the only invocation, provider capability, credential lease and
// wire codec exposed by a proxy instance. Bearer authenticates OpenCode to this
// local endpoint and is unrelated to the upstream credential.
type Config struct {
	AccessJournalPath  string
	GatewayJournalPath string
	Policy             access.Policy
	Intent             access.Intent
	Gateway            providergateway.Binding
	Credential         *providercredential.Lease
	Transport          *providertransport.Client
	Bearer             string
	Expectation        providergateway.AdapterRequestExpectation
	HandlerTimeout     time.Duration
}

// Binding is the non-secret, immutable capability identity served locally.
type Binding struct {
	Version              int    `json:"version"`
	AccessPolicyID       string `json:"access_policy_id"`
	AccessInvocationID   string `json:"access_invocation_id"`
	GatewayBindingID     string `json:"gateway_binding_id"`
	CredentialBindingID  string `json:"credential_binding_id"`
	CapabilitySHA256     string `json:"capability_sha256"`
	AdapterID            string `json:"adapter_id"`
	RequestPath          string `json:"request_path"`
	RequestExpectationID string `json:"request_expectation_id"`
	MaximumRequestBytes  int64  `json:"maximum_request_bytes"`
	MaximumResponseBytes int64  `json:"maximum_response_bytes"`
}

// ID validates and hashes the complete non-secret proxy capability.
func (b Binding) ID() (string, error) {
	for _, digest := range []string{b.AccessPolicyID, b.AccessInvocationID, b.GatewayBindingID, b.CredentialBindingID, b.CapabilitySHA256, b.RequestExpectationID} {
		if safepath.RequireDigest(digest) != nil {
			return "", errors.New("invalid provider proxy binding")
		}
	}
	if b.Version != 1 || !identifier(b.AdapterID) || !localRequestPath(b.RequestPath) || b.MaximumRequestBytes < 2 || b.MaximumRequestBytes > 1<<20 || b.MaximumResponseBytes < 1 || b.MaximumResponseBytes > 8<<20 {
		return "", errors.New("invalid provider proxy binding")
	}
	return canonical.Hash("harness.provider-proxy-binding.v1", b)
}

// Server is immutable except for listener and lifecycle state.
type Server struct {
	accessPath  string
	gatewayPath string
	policy      access.Policy
	intent      access.Intent
	gateway     providergateway.Binding
	expectation providergateway.AdapterRequestExpectation
	credential  *providercredential.Lease
	transport   *providertransport.Client
	execute     func(context.Context, providertransport.Request) (providertransport.Result, error)
	binding     Binding
	bindingID   string
	token       []byte
	tokenHash   [sha256.Size]byte
	timeout     time.Duration

	mu            sync.Mutex
	listened      bool
	closing       bool
	poisoned      bool
	terminal      bool
	active        int
	dispatched    bool
	cacheKeyBound bool
	expectedHost  string
	settled       chan struct{}
	settledOnce   sync.Once
}

// New validates and freezes one exact proxy capability. It performs no network
// operation and does not expose or consume the upstream credential bytes.
func New(config Config) (*Server, error) {
	if config.Transport == nil || config.Credential == nil || !cleanAbsolute(config.AccessJournalPath) || !cleanAbsolute(config.GatewayJournalPath) || strings.EqualFold(config.AccessJournalPath, config.GatewayJournalPath) || !validBearer(config.Bearer) {
		return nil, errors.New("invalid provider proxy configuration")
	}
	policy, intent, gateway, expectation, err := freezeConfig(config)
	if err != nil {
		return nil, err
	}
	policyID, err := policy.ID()
	if err != nil || policyID != gateway.AccessPolicyID || intent.PolicyID != policyID {
		return nil, errors.New("provider proxy policy differs from gateway")
	}
	invocationID, err := intent.ID()
	if err != nil || invocationID != intent.Reservation.InvocationID || invocationID != gateway.AccessInvocationID {
		return nil, errors.New("provider proxy invocation differs from gateway")
	}
	gatewayID, err := gateway.ID()
	if err != nil || gateway.Endpoint.Version != 2 || gateway.Model.Version != 2 || gateway.Endpoint.AdapterID != gateway.Model.AdapterID {
		return nil, errors.New("provider proxy requires one executable v2 adapter")
	}
	if err := access.RequireActive(config.AccessJournalPath, policy, intent); err != nil {
		return nil, errors.New("provider proxy requires active access")
	}
	gatewayState, err := providergateway.Inspect(config.GatewayJournalPath)
	if err != nil || gatewayState.Binding == nil || !equalCanonical(*gatewayState.Binding, gateway) || len(gatewayState.Calls) != 0 || gatewayState.Pending != nil || gatewayState.Finished || gatewayState.Exhausted {
		return nil, errors.New("provider proxy requires an unused durable gateway binding")
	}
	credential := config.Credential.Binding()
	credentialID, err := credential.ID()
	if err != nil || !credentialMatches(credential, gateway, intent) {
		return nil, errors.New("provider proxy credential differs from gateway")
	}
	expectationID, err := providergateway.AdapterRequestExpectationID(gateway, expectation)
	if err != nil {
		return nil, err
	}
	requestPath, err := providergateway.AdapterRequestPath(gateway)
	if err != nil || !localRequestPath(requestPath) {
		return nil, errors.New("invalid provider proxy request path")
	}
	capabilitySHA256, err := opencode.ProviderCapabilitySHA256(config.Bearer)
	if err != nil {
		return nil, errors.New("invalid provider proxy capability")
	}
	timeout := config.HandlerTimeout
	if timeout == 0 {
		timeout = defaultHandlerTimeout
	}
	if timeout <= 0 || timeout > maximumHandlerTimeout {
		return nil, errors.New("invalid provider proxy handler timeout")
	}
	binding := Binding{Version: 1, AccessPolicyID: policyID, AccessInvocationID: invocationID, GatewayBindingID: gatewayID, CredentialBindingID: credentialID, CapabilitySHA256: capabilitySHA256, AdapterID: gateway.Model.AdapterID, RequestPath: requestPath, RequestExpectationID: expectationID, MaximumRequestBytes: expectation.MaxBytes, MaximumResponseBytes: gateway.Model.MaxResponseBytes}
	bindingID, err := binding.ID()
	if err != nil {
		return nil, err
	}
	token := []byte(config.Bearer)
	server := &Server{accessPath: config.AccessJournalPath, gatewayPath: config.GatewayJournalPath, policy: policy, intent: intent, gateway: gateway, expectation: expectation, credential: config.Credential, transport: config.Transport, binding: binding, bindingID: bindingID, token: append([]byte(nil), token...), tokenHash: sha256.Sum256(token), timeout: timeout, settled: make(chan struct{})}
	server.execute = server.transport.Execute
	return server, nil
}

// Running is the exact listener owned by one Server.
type Running struct {
	owner    *Server
	server   *http.Server
	listener net.Listener
	url      string
	done     chan struct{}
	mu       sync.Mutex
	waitErr  error
}

// Listen binds a fresh IPv4 loopback port. No address is caller-configurable.
func (s *Server) Listen() (*Running, error) {
	if s == nil {
		return nil, errors.New("provider proxy server required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listened || s.closing {
		return nil, errors.New("provider proxy already listened or closed")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.listened = true
	s.expectedHost = listener.Addr().String()
	running := &Running{owner: s, listener: listener, url: "http://" + s.expectedHost + s.binding.RequestPath, done: make(chan struct{})}
	running.server = &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: s.timeout, WriteTimeout: s.timeout, IdleTimeout: 30 * time.Second, MaxHeaderBytes: maximumHeaderBytes}
	go func() {
		err := running.server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		running.owner.stopAdmission()
		running.mu.Lock()
		running.waitErr = err
		running.mu.Unlock()
		close(running.done)
	}()
	return running, nil
}

// URL returns the exact controller-owned loopback provider endpoint.
func (r *Running) URL() string {
	if r == nil {
		return ""
	}
	return r.url
}

// Binding returns the immutable non-secret capability and its validated ID.
func (r *Running) Binding() (Binding, string) {
	if r == nil || r.owner == nil {
		return Binding{}, ""
	}
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	return r.owner.binding, r.owner.bindingID
}

// BindResponsesPromptCacheKey binds one generated session identity into the
// exact Responses request expectation before any local request is dispatched.
// The updated expectation and proxy capability IDs remain immutable afterward.
func (r *Running) BindResponsesPromptCacheKey(sessionID string) error {
	if r == nil || r.owner == nil || !identifier(sessionID) {
		return errors.New("invalid Responses prompt cache session identity")
	}
	s := r.owner
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.listened || s.closing || s.poisoned || s.terminal || s.active != 0 || s.dispatched || s.cacheKeyBound || s.binding.AdapterID != providergateway.OpenAIResponsesAdapter {
		return errors.New("provider proxy cannot bind Responses prompt cache key")
	}
	var controls providergateway.ResponsesRequestExpectation
	if len(s.expectation.Controls) == 0 || json.Unmarshal(s.expectation.Controls, &controls) != nil || controls.PromptCacheKey != "" {
		return errors.New("Responses prompt cache key is already configured or invalid")
	}
	controls.PromptCacheKey = sessionID
	raw, err := json.Marshal(controls)
	if err != nil {
		return errors.New("cannot encode Responses prompt cache key")
	}
	next := s.expectation
	next.Controls = raw
	expectationID, err := providergateway.AdapterRequestExpectationID(s.gateway, next)
	if err != nil {
		return err
	}
	nextBinding := s.binding
	nextBinding.RequestExpectationID = expectationID
	nextBindingID, err := nextBinding.ID()
	if err != nil {
		return err
	}
	s.expectation = next
	s.binding = nextBinding
	s.bindingID = nextBindingID
	s.cacheKeyBound = true
	return nil
}

// ProviderSealIdentity validates the exact scoped capability and returns the
// secret-free proxy identity used by OpenCode's provider seal.
func (r *Running) ProviderSealIdentity(capability string) (opencode.ProviderProxyIdentity, error) {
	if r == nil || r.owner == nil {
		return opencode.ProviderProxyIdentity{}, errors.New("running provider proxy required")
	}
	r.owner.mu.Lock()
	binding, bindingID := r.owner.binding, r.owner.bindingID
	r.owner.mu.Unlock()
	digest, err := opencode.ProviderCapabilitySHA256(capability)
	if err != nil || subtle.ConstantTimeCompare([]byte(digest), []byte(binding.CapabilitySHA256)) != 1 {
		return opencode.ProviderProxyIdentity{}, errors.New("provider proxy capability mismatch")
	}
	baseURL := strings.TrimSuffix(r.url, strings.TrimPrefix(binding.RequestPath, "/v1"))
	identity := opencode.ProviderProxyIdentity{Version: 1, BindingSHA256: bindingID, CapabilitySHA256: binding.CapabilitySHA256, URL: r.url, BaseURL: baseURL, AdapterID: binding.AdapterID, AccessInvocationID: binding.AccessInvocationID, GatewayBindingID: binding.GatewayBindingID}
	identity.SHA256, err = canonical.Hash("harness.opencode-provider-proxy-identity.v1", identity)
	if err != nil || opencode.ValidateProviderProxyIdentity(identity) != nil {
		return opencode.ProviderProxyIdentity{}, errors.New("invalid provider proxy seal identity")
	}
	return identity, nil
}

// ValidateOwner proves in-process linkage to the exact transport, credential
// lease and local bearer used to construct this listener.
func (r *Running) ValidateOwner(transport *providertransport.Client, credential *providercredential.Lease, bearer string) error {
	if r == nil || r.owner == nil || transport == nil || credential == nil || r.owner.transport != transport || r.owner.credential != credential {
		return errors.New("foreign provider proxy owner")
	}
	digest := sha256.Sum256([]byte(bearer))
	currentCredentialID, err := credential.Binding().ID()
	r.owner.mu.Lock()
	credentialBindingID, tokenHash := r.owner.binding.CredentialBindingID, r.owner.tokenHash
	r.owner.mu.Unlock()
	if err != nil || currentCredentialID != credentialBindingID || subtle.ConstantTimeCompare(digest[:], tokenHash[:]) != 1 {
		return errors.New("provider proxy owner identity changed")
	}
	return nil
}

// Close stops admission then waits for active HTTP handlers through Shutdown.
// A deadline failure force-closes connections; begun provider calls remain
// unresolved and are never retried by this proxy.
func (r *Running) Close(ctx context.Context) error {
	if r == nil || r.owner == nil || r.server == nil || ctx == nil {
		return errors.New("running provider proxy required")
	}
	r.owner.stopAdmission()
	err := r.server.Shutdown(ctx)
	if err != nil {
		err = errors.Join(err, r.server.Close())
	}
	return err
}

// Wait returns after both the listener and all admitted handlers settle.
func (r *Running) Wait() error {
	if r == nil || r.owner == nil || r.done == nil {
		return errors.New("running provider proxy required")
	}
	<-r.done
	<-r.owner.settled
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.waitErr
}

// ServeHTTP admits one exact local provider request and forwards only a
// transport-validated response whose completion is already durable.
func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if !s.markDispatched() {
		http.Error(writer, "provider proxy unavailable", http.StatusConflict)
		return
	}
	if err := s.validateHTTP(request); err != nil {
		http.Error(writer, "provider request rejected", http.StatusBadRequest)
		return
	}
	if !s.beginHandler() {
		http.Error(writer, "provider proxy unavailable", http.StatusConflict)
		return
	}
	defer s.endHandler()
	expectationID, expectationErr := providergateway.AdapterRequestExpectationID(s.gateway, s.expectation)
	if expectationErr != nil || expectationID != s.binding.RequestExpectationID {
		s.poison()
		http.Error(writer, "provider proxy unavailable", http.StatusConflict)
		return
	}
	body, err := readExactBody(writer, request, s.binding.MaximumRequestBytes)
	if err != nil {
		http.Error(writer, "provider request rejected", http.StatusBadRequest)
		return
	}
	state, err := providergateway.Inspect(s.gatewayPath)
	if err != nil || state.Binding == nil || state.Pending != nil || state.Finished || state.Exhausted {
		s.poison()
		http.Error(writer, "provider proxy unavailable", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.timeout)
	defer cancel()
	result, err := s.execute(ctx, providertransport.Request{AccessJournalPath: s.accessPath, GatewayJournalPath: s.gatewayPath, Policy: s.policy, Intent: s.intent, Binding: s.gateway, Lease: s.credential, Body: body, Expectation: s.expectation})
	if err != nil {
		s.poison()
		http.Error(writer, "provider call unresolved", http.StatusBadGateway)
		return
	}
	state, err = providergateway.Inspect(s.gatewayPath)
	if err != nil || state.Binding == nil || state.Pending != nil || len(state.Calls) == 0 || state.Calls[len(state.Calls)-1].Receipt == nil || !equalReceipt(*state.Calls[len(state.Calls)-1].Receipt, result.Receipt) {
		s.poison()
		http.Error(writer, "provider completion unavailable", http.StatusBadGateway)
		return
	}
	if state.Finished || state.Exhausted {
		s.markTerminal()
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	written, writeErr := writer.Write(result.Body)
	if writeErr != nil || written != len(result.Body) {
		s.poison()
	}
}

func (s *Server) markDispatched() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.poisoned || s.terminal {
		return false
	}
	s.dispatched = true
	return true
}

func (s *Server) validateHTTP(request *http.Request) error {
	if request == nil || request.Method != http.MethodPost || request.TLS != nil || request.URL == nil || request.URL.Path != s.binding.RequestPath || request.URL.RawPath != "" || request.URL.RawQuery != "" || request.URL.ForceQuery || request.RequestURI != s.binding.RequestPath || request.Host != s.currentHost() || !loopbackPeer(request.RemoteAddr) {
		return errors.New("invalid local provider request")
	}
	if request.ContentLength < 2 || request.ContentLength > s.binding.MaximumRequestBytes || len(request.TransferEncoding) != 0 || len(request.Trailer) != 0 {
		return errors.New("invalid local provider body framing")
	}
	if !authorized(request.Header.Values("Authorization"), s.tokenHash) || !contentTypeJSON(request.Header.Values("Content-Type")) || !accepted(request.Header.Values("Accept")) {
		return errors.New("invalid local provider headers")
	}
	for name := range request.Header {
		lower := strings.ToLower(name)
		if lower == "cookie" || lower == "proxy-authorization" || lower == "content-encoding" || lower == "upgrade" || lower == "forwarded" || strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "proxy-") || (s.gateway.Endpoint.Auth != nil && s.gateway.Endpoint.Auth.HeaderName != "" && strings.EqualFold(lower, s.gateway.Endpoint.Auth.HeaderName)) || s.gateway.Endpoint.SessionHeader != "" && strings.EqualFold(lower, s.gateway.Endpoint.SessionHeader) {
			return errors.New("forbidden local provider header")
		}
	}
	return nil
}

func (s *Server) currentHost() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expectedHost
}

func (s *Server) beginHandler() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.poisoned || s.terminal || s.active != 0 {
		return false
	}
	s.active = 1
	return true
}

func (s *Server) endHandler() {
	s.mu.Lock()
	s.active = 0
	settled := s.closing
	s.mu.Unlock()
	if settled {
		s.settledOnce.Do(func() { close(s.settled) })
	}
}

func (s *Server) poison() {
	s.mu.Lock()
	s.poisoned = true
	s.mu.Unlock()
}

func (s *Server) markTerminal() {
	s.mu.Lock()
	s.terminal = true
	s.mu.Unlock()
}

func (s *Server) stopAdmission() {
	s.mu.Lock()
	s.closing = true
	settled := s.active == 0
	clear(s.token)
	s.mu.Unlock()
	if settled {
		s.settledOnce.Do(func() { close(s.settled) })
	}
}

func freezeConfig(config Config) (access.Policy, access.Intent, providergateway.Binding, providergateway.AdapterRequestExpectation, error) {
	var policy access.Policy
	var intent access.Intent
	var gateway providergateway.Binding
	for _, item := range []struct{ source, target any }{{config.Policy, &policy}, {config.Intent, &intent}, {config.Gateway, &gateway}} {
		raw, err := canonical.Bytes(item.source)
		if err != nil || canonical.Decode(raw, item.target) != nil {
			return policy, intent, gateway, providergateway.AdapterRequestExpectation{}, errors.New("invalid provider proxy configuration")
		}
	}
	expectation := config.Expectation
	expectation.Controls = append(json.RawMessage(nil), config.Expectation.Controls...)
	expectation.Tools = append([]providergateway.RequestTool(nil), config.Expectation.Tools...)
	for index := range expectation.Tools {
		expectation.Tools[index].Parameters = append(json.RawMessage(nil), expectation.Tools[index].Parameters...)
	}
	if config.Expectation.RequiredCapabilities != nil {
		required := *config.Expectation.RequiredCapabilities
		required.Schema = append(json.RawMessage(nil), required.Schema...)
		expectation.RequiredCapabilities = &required
	}
	if config.Expectation.TerminalStructuredOutput != nil {
		terminal := *config.Expectation.TerminalStructuredOutput
		terminal.Schema = append(json.RawMessage(nil), terminal.Schema...)
		expectation.TerminalStructuredOutput = &terminal
	}
	return policy, intent, gateway, expectation, nil
}

func credentialMatches(credential providercredential.Binding, gateway providergateway.Binding, intent access.Intent) bool {
	return gateway.Endpoint.Auth != nil && credential.AccessPolicyID == gateway.AccessPolicyID && credential.AccessInvocationID == gateway.AccessInvocationID && credential.RouteID == gateway.RouteID && credential.AccessProfileID == intent.Route.AccessID && credential.Provider == gateway.Endpoint.Provider && credential.Runtime == intent.Route.Runtime && credential.EndpointID == gateway.EndpointID && credential.CredentialRef == gateway.Endpoint.Auth.CredentialRef && credential.AuthScheme == gateway.Endpoint.Auth.Scheme && credential.BillingMode == intent.Reservation.BillingMode
}

func readExactBody(writer http.ResponseWriter, request *http.Request, maximum int64) ([]byte, error) {
	reader := http.MaxBytesReader(writer, request.Body, maximum)
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil || int64(len(body)) != request.ContentLength {
		return nil, errors.New("invalid local provider body")
	}
	return body, nil
}

func authorized(values []string, expected [sha256.Size]byte) bool {
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return false
	}
	actual := sha256.Sum256([]byte(values[0][len("Bearer "):]))
	return subtle.ConstantTimeCompare(actual[:], expected[:]) == 1
}

func contentTypeJSON(values []string) bool {
	if len(values) != 1 {
		return false
	}
	kind, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || kind != "application/json" || len(parameters) > 1 {
		return false
	}
	charset, present := parameters["charset"]
	return len(parameters) == 0 || present && strings.EqualFold(charset, "utf-8")
}

func accepted(values []string) bool {
	if len(values) != 1 {
		return false
	}
	switch strings.TrimSpace(values[0]) {
	case "*/*", "text/event-stream", "text/event-stream, */*", "application/json":
		return true
	default:
		return false
	}
}

func loopbackPeer(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.To4() != nil && ip.String() == "127.0.0.1"
}

func validBearer(value string) bool {
	if len(value) < 32 || len(value) > 512 || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func localRequestPath(value string) bool {
	return len(value) > 1 && len(value) <= 4096 && value[0] == '/' && !strings.ContainsAny(value, "?#\\") && filepath.ToSlash(filepath.Clean(value)) == value
}

func cleanAbsolute(value string) bool {
	return value != "" && len(value) <= 4096 && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func identifier(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.') {
			return false
		}
	}
	return true
}

func equalReceipt(left, right providergateway.CallReceipt) bool {
	return reflect.DeepEqual(left, right)
}

func equalCanonical(left, right any) bool {
	a, err := canonical.Bytes(left)
	if err != nil {
		return false
	}
	b, err := canonical.Bytes(right)
	return err == nil && bytes.Equal(a, b)
}
