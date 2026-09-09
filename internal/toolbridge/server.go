// Package toolbridge implements the sessionless JSON-response subset of MCP
// Streamable HTTP. It pins MCP 2025-11-25, binds only 127.0.0.1, advertises only
// tools, sends no server-initiated messages, and never assigns MCP session IDs.
// Controller callbacks retain all authority, admission, journaling and execution
// policy; this transport only validates and frames authenticated MCP messages.
package toolbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ProtocolVersion is the sole MCP revision admitted by this transport.
const ProtocolVersion = "2025-11-25"

const (
	defaultEndpoint          = "/mcp"
	defaultMaxRequestBytes   = int64(64 << 10)
	defaultMaxResponseBytes  = 768 << 10
	defaultMaxConcurrentCall = 1
	defaultCallTimeout       = 15 * time.Second
	maximumBodyBound         = 1 << 20
)

var (
	toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	versionPattern  = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

// ToolDefinition is the controller-supplied, provider-neutral tool catalog row.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Call is an admitted tool invocation. Arguments is always one JSON object.
type Call struct {
	RequestID json.RawMessage
	Tool      string
	Arguments json.RawMessage
}

// Observation records only an admitted protocol operation. CatalogHash is set
// for tools/list so integration evidence binds the exact catalog returned.
type Observation struct {
	Method       string
	Notification bool
	CatalogHash  string
}

// ToolError is bounded, controller-redacted feedback for a domain failure.
type ToolError struct {
	Code    string
	Message string
}

// Result contains canonical JSON for a successful call, or a domain error.
type Result struct {
	JSON    json.RawMessage
	IsError bool
	Error   *ToolError
}

// CatalogFunc supplies the controller's current admitted tool catalog.
type CatalogFunc func() ([]ToolDefinition, error)

// CallFunc executes a validated request under controller-owned authority.
type CallFunc func(context.Context, Call) (Result, error)

// ObserveFunc records an admitted protocol operation without executing a tool.
type ObserveFunc func(Observation)

// Config supplies the immutable catalog and controller-owned call callback.
type Config struct {
	Token              string
	Endpoint           string
	AllowedOrigins     []string
	Catalog            CatalogFunc
	Call               CallFunc
	Observe            ObserveFunc
	MaxRequestBytes    int64
	MaxResponseBytes   int
	MaxConcurrentCalls int
	MaxQueuedCalls     int
	CallTimeout        time.Duration
}

// Server is safe for concurrent use and may be listened once.
type Server struct {
	endpoint           string
	token              []byte
	tokenHash          [sha256.Size]byte
	allowedOrigins     map[string]struct{}
	tools              []ToolDefinition
	toolNames          map[string]struct{}
	catalogHash        string
	call               CallFunc
	observe            ObserveFunc
	maxRequestBytes    int64
	maxResponseBytes   int
	callTimeout        time.Duration
	maxConcurrentCalls int
	maxQueuedCalls     int

	mu           sync.Mutex
	expectedHost string
	listened     bool
	active       map[string]context.CancelFunc
	runningCalls int
	queuedCalls  []*callAdmission
	closing      bool
}

// New validates and deep-copies the catalog exactly once. Later controller
// changes cannot alter the catalog exposed by this server.
func New(config Config) (*Server, error) {
	if config.Catalog == nil || config.Call == nil {
		return nil, errors.New("tool bridge callbacks required")
	}
	if len(config.Token) < 32 || len(config.Token) > 512 || strings.TrimSpace(config.Token) != config.Token || !utf8.ValidString(config.Token) {
		return nil, errors.New("invalid tool bridge credential")
	}
	for _, character := range config.Token {
		if character < 0x21 || character > 0x7e {
			return nil, errors.New("invalid tool bridge credential")
		}
	}
	endpoint := config.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	if endpoint[0] != '/' || endpoint == "/" || strings.ContainsAny(endpoint, "?#") || strings.Contains(endpoint[1:], "/") {
		return nil, errors.New("invalid MCP endpoint")
	}
	maxRequest := config.MaxRequestBytes
	if maxRequest == 0 {
		maxRequest = defaultMaxRequestBytes
	}
	maxResponse := config.MaxResponseBytes
	if maxResponse == 0 {
		maxResponse = defaultMaxResponseBytes
	}
	maxConcurrent := config.MaxConcurrentCalls
	if maxConcurrent == 0 {
		maxConcurrent = defaultMaxConcurrentCall
	}
	callTimeout := config.CallTimeout
	if callTimeout == 0 {
		callTimeout = defaultCallTimeout
	}
	if maxRequest < 1024 || maxRequest > maximumBodyBound || maxResponse < 1024 || maxResponse > maximumBodyBound || maxConcurrent < 1 || maxConcurrent > 32 || config.MaxQueuedCalls < 0 || config.MaxQueuedCalls > 32 || callTimeout <= 0 || callTimeout > time.Minute {
		return nil, errors.New("invalid tool bridge bounds")
	}

	origins := make(map[string]struct{}, len(config.AllowedOrigins))
	for _, origin := range config.AllowedOrigins {
		if origin == "" || strings.TrimSpace(origin) != origin || strings.ContainsAny(origin, "\r\n") {
			return nil, errors.New("invalid allowed origin")
		}
		origins[origin] = struct{}{}
	}

	catalog, err := config.Catalog()
	if err != nil {
		return nil, errors.New("tool catalog callback failed")
	}
	tools, names, catalogHash, err := validateCatalog(catalog, maxResponse)
	if err != nil {
		return nil, err
	}
	token := append([]byte(nil), config.Token...)
	return &Server{
		endpoint: endpoint, token: token, tokenHash: sha256.Sum256(token), allowedOrigins: origins,
		tools: tools, toolNames: names, catalogHash: catalogHash, call: config.Call, observe: config.Observe,
		maxRequestBytes: maxRequest, maxResponseBytes: maxResponse, callTimeout: callTimeout,
		maxConcurrentCalls: maxConcurrent, maxQueuedCalls: config.MaxQueuedCalls, active: map[string]context.CancelFunc{},
	}, nil
}

func validateCatalog(catalog []ToolDefinition, maxResponse int) ([]ToolDefinition, map[string]struct{}, string, error) {
	if len(catalog) == 0 || len(catalog) > 128 {
		return nil, nil, "", errors.New("invalid tool catalog size")
	}
	tools := make([]ToolDefinition, 0, len(catalog))
	names := make(map[string]struct{}, len(catalog))
	for _, tool := range catalog {
		if len(tool.Name) < 1 || len(tool.Name) > 128 || !toolNamePattern.MatchString(tool.Name) {
			return nil, nil, "", errors.New("invalid tool catalog name")
		}
		if _, exists := names[tool.Name]; exists {
			return nil, nil, "", errors.New("duplicate tool catalog name")
		}
		if len(tool.Description) > 4096 || !utf8.ValidString(tool.Description) {
			return nil, nil, "", errors.New("invalid tool description")
		}
		schema := append(json.RawMessage(nil), tool.InputSchema...)
		if len(schema) == 0 || len(schema) > 32<<10 || !json.Valid(schema) || firstJSONByte(schema) != '{' {
			return nil, nil, "", errors.New("invalid tool input schema")
		}
		var shape struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(schema, &shape); err != nil || shape.Type != "object" {
			return nil, nil, "", errors.New("tool input schema must describe an object")
		}
		names[tool.Name] = struct{}{}
		tools = append(tools, ToolDefinition{Name: tool.Name, Description: tool.Description, InputSchema: schema})
	}
	wire, err := json.Marshal(struct {
		Tools []ToolDefinition `json:"tools"`
	}{tools})
	if err != nil || len(wire) > maxResponse {
		return nil, nil, "", errors.New("tool catalog exceeds response bound")
	}
	hash := sha256.Sum256(wire)
	return tools, names, hex.EncodeToString(hash[:]), nil
}

// CatalogHash identifies the exact deep-copied catalog served by this instance.
func (s *Server) CatalogHash() string { return s.catalogHash }

// Running is a loopback listener owned by the bridge.
type Running struct {
	URL      string
	owner    *Server
	server   *http.Server
	listener net.Listener
	done     chan error
}

// Listen binds a fresh IPv4 loopback port and starts serving. No remote address
// or wildcard binding is configurable.
func (s *Server) Listen() (*Running, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listened {
		return nil, errors.New("tool bridge already listened")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.listened = true
	s.expectedHost = listener.Addr().String()
	httpServer := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      s.callTimeout + 5*time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	running := &Running{URL: "http://" + s.expectedHost + s.endpoint, owner: s, server: httpServer, listener: listener, done: make(chan error, 1)}
	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		running.done <- err
		close(running.done)
	}()
	return running, nil
}

// Close stops admission, cancels in-flight callbacks, and shuts down within the
// caller's deadline.
func (r *Running) Close(ctx context.Context) error {
	r.owner.stop()
	return r.server.Shutdown(ctx)
}

// Wait returns after the listener exits.
func (r *Running) Wait() error { return <-r.done }

// ServeHTTP authenticates and dispatches one bounded MCP protocol request.
func (s *Server) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	s.responseHeaders(w)
	if request.URL.Path != s.endpoint || request.URL.RawQuery != "" {
		http.NotFound(w, request)
		return
	}
	if !s.validHost(request.Host) {
		http.Error(w, "invalid host", http.StatusMisdirectedRequest)
		return
	}
	if origin := request.Header.Get("Origin"); origin != "" {
		if len(request.Header.Values("Origin")) != 1 {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if _, allowed := s.allowedOrigins[origin]; !allowed {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
	}
	if !s.authorized(request.Header.Values("Authorization")) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="mcp"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.isClosing() {
		http.Error(w, "server closing", http.StatusServiceUnavailable)
		return
	}
	if request.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !accepts(request.Header.Values("Accept"), "application/json") || !accepts(request.Header.Values("Accept"), "text/event-stream") {
		http.Error(w, "required Accept media types missing", http.StatusNotAcceptable)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	body, status, err := boundedBody(request.Body, request.ContentLength, s.maxRequestBytes)
	if err != nil {
		http.Error(w, "request body exceeds bound", status)
		return
	}
	if !utf8.Valid(body) || !unambiguousJSON(body) {
		s.writeRPCError(w, http.StatusBadRequest, nil, -32700, "ambiguous JSON-RPC message")
		return
	}
	var message rpcMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&message); err != nil || message.JSONRPC != "2.0" || message.Method == "" {
		s.writeRPCError(w, http.StatusBadRequest, nil, -32700, "invalid JSON-RPC message")
		return
	}
	if message.Method == "initialize" {
		if header := request.Header.Get("MCP-Protocol-Version"); header != "" && !versionPattern.MatchString(header) {
			http.Error(w, "invalid MCP protocol version", http.StatusBadRequest)
			return
		}
	} else if request.Header.Get("MCP-Protocol-Version") != ProtocolVersion {
		http.Error(w, "unsupported MCP protocol version", http.StatusBadRequest)
		return
	}
	if message.ID == nil {
		s.notification(w, message)
		return
	}
	canonicalID, _, valid := canonicalRequestID(message.ID)
	if !valid {
		s.writeRPCError(w, http.StatusBadRequest, nil, -32600, "invalid request id")
		return
	}
	s.request(w, request, message, canonicalID)
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (s *Server) request(w http.ResponseWriter, request *http.Request, message rpcMessage, canonicalID json.RawMessage) {
	switch message.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string          `json:"protocolVersion"`
			Capabilities    json.RawMessage `json:"capabilities"`
			ClientInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		if len(message.Params) == 0 || firstJSONByte(message.Params) != '{' || json.Unmarshal(message.Params, &params) != nil || !versionPattern.MatchString(params.ProtocolVersion) || firstJSONByte(params.Capabilities) != '{' || params.ClientInfo.Name == "" || params.ClientInfo.Version == "" {
			s.writeRPCError(w, http.StatusOK, message.ID, -32602, "invalid initialize parameters")
			return
		}
		s.observed(Observation{Method: message.Method})
		s.writeResult(w, message.ID, map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "engorch-tool-bridge", "version": "1"},
		})
	case "ping":
		if !emptyParams(message.Params) {
			s.writeRPCError(w, http.StatusOK, message.ID, -32602, "invalid ping parameters")
			return
		}
		s.observed(Observation{Method: message.Method})
		s.writeResult(w, message.ID, map[string]any{})
	case "tools/list":
		var params struct {
			Cursor string          `json:"cursor,omitempty"`
			Meta   json.RawMessage `json:"_meta,omitempty"`
		}
		if decodeObjectDefault(message.Params, &params) != nil || params.Cursor != "" {
			s.writeRPCError(w, http.StatusOK, message.ID, -32602, "invalid tools/list parameters")
			return
		}
		s.observed(Observation{Method: message.Method, CatalogHash: s.catalogHash})
		s.writeResult(w, message.ID, struct {
			Tools []ToolDefinition `json:"tools"`
		}{s.tools})
	case "tools/call":
		s.callTool(w, request, message, canonicalID)
	default:
		s.writeRPCError(w, http.StatusOK, message.ID, -32601, "method not found")
	}
}

func (s *Server) notification(w http.ResponseWriter, message rpcMessage) {
	switch message.Method {
	case "notifications/initialized":
		if !objectOrAbsent(message.Params) {
			s.writeRPCError(w, http.StatusBadRequest, nil, -32602, "invalid initialized notification")
			return
		}
		s.observed(Observation{Method: message.Method, Notification: true})
	case "notifications/cancelled":
		var params struct {
			RequestID json.RawMessage `json:"requestId"`
			Reason    string          `json:"reason,omitempty"`
			Meta      json.RawMessage `json:"_meta,omitempty"`
		}
		if decodeObject(message.Params, &params, true) != nil {
			s.writeRPCError(w, http.StatusBadRequest, nil, -32602, "invalid cancellation notification")
			return
		}
		_, key, valid := canonicalRequestID(params.RequestID)
		if !valid || len(params.Reason) > 1024 {
			s.writeRPCError(w, http.StatusBadRequest, nil, -32602, "invalid cancellation notification")
			return
		}
		s.observed(Observation{Method: message.Method, Notification: true})
		s.cancel(key)
	default:
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) callTool(w http.ResponseWriter, request *http.Request, message rpcMessage, canonicalID json.RawMessage) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
		Meta      json.RawMessage `json:"_meta,omitempty"`
	}
	if decodeObject(message.Params, &params, true) != nil || params.Name == "" {
		s.writeRPCError(w, http.StatusOK, message.ID, -32602, "invalid tools/call parameters")
		return
	}
	if params.Arguments == nil {
		params.Arguments = json.RawMessage(`{}`)
	}
	if firstJSONByte(params.Arguments) != '{' || !json.Valid(params.Arguments) {
		s.writeRPCError(w, http.StatusOK, message.ID, -32602, "tool arguments must be an object")
		return
	}
	if _, allowed := s.toolNames[params.Name]; !allowed {
		s.writeRPCError(w, http.StatusOK, message.ID, -32602, "unknown tool")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.callTimeout)
	key := string(canonicalID)
	admission, status := s.admitCall(key, cancel, ctx)
	if status == callAdmissionFull {
		cancel()
		s.writeRPCError(w, http.StatusTooManyRequests, message.ID, -32000, "tool call concurrency limit reached")
		return
	}
	if status == callAdmissionDuplicate {
		cancel()
		s.writeRPCError(w, http.StatusOK, message.ID, -32600, "duplicate active request id")
		return
	}
	if status == callAdmissionClosing {
		cancel()
		http.Error(w, "server closing", http.StatusServiceUnavailable)
		return
	}
	defer func() { s.finishCall(admission); cancel() }()
	if !s.waitForCall(admission) {
		s.writeRPCError(w, http.StatusOK, message.ID, -32800, "request cancelled or timed out")
		return
	}
	s.observed(Observation{Method: message.Method})
	result, err := s.call(ctx, Call{RequestID: append(json.RawMessage(nil), canonicalID...), Tool: params.Name, Arguments: append(json.RawMessage(nil), params.Arguments...)})
	if ctx.Err() != nil {
		s.writeRPCError(w, http.StatusOK, message.ID, -32800, "request cancelled or timed out")
		return
	}
	if err != nil {
		s.writeRPCError(w, http.StatusOK, message.ID, -32603, "tool callback failed")
		return
	}
	if result.Error != nil && len(result.JSON) != 0 {
		s.writeRPCError(w, http.StatusOK, message.ID, -32603, "ambiguous tool result")
		return
	}
	if result.Error != nil {
		if !validToolError(*result.Error) || s.containsCredential([]byte(result.Error.Code+result.Error.Message)) {
			s.writeRPCError(w, http.StatusOK, message.ID, -32603, "invalid tool error result")
			return
		}
		s.writeResult(w, message.ID, map[string]any{
			"content": []any{map[string]any{"type": "text", "text": result.Error.Code + ": " + result.Error.Message}},
			"isError": true,
		})
		return
	}
	if s.containsCredential(result.JSON) {
		s.writeRPCError(w, http.StatusOK, message.ID, -32603, "invalid tool result")
		return
	}
	wire, err := EncodeToolResult(message.ID, result)
	if err != nil {
		s.writeRPCError(w, http.StatusOK, message.ID, -32603, "invalid tool result")
		return
	}
	s.writeEncodedToolResult(w, message.ID, wire)
}

func (s *Server) observed(observation Observation) {
	if s.observe != nil {
		s.observe(observation)
	}
}

func (s *Server) cancel(key string) {
	s.mu.Lock()
	cancel := s.active[key]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Server) stop() {
	s.mu.Lock()
	s.closing = true
	cancels := make([]context.CancelFunc, 0, len(s.active))
	for _, cancel := range s.active {
		cancels = append(cancels, cancel)
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (s *Server) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

func (s *Server) validHost(host string) bool {
	s.mu.Lock()
	expected := s.expectedHost
	s.mu.Unlock()
	return expected != "" && host == expected
}

func (s *Server) authorized(values []string) bool {
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return false
	}
	presented := sha256.Sum256([]byte(strings.TrimPrefix(values[0], "Bearer ")))
	return subtle.ConstantTimeCompare(presented[:], s.tokenHash[:]) == 1
}

func (s *Server) containsCredential(data []byte) bool {
	return len(s.token) != 0 && bytes.Contains(data, s.token)
}

func (s *Server) responseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func (s *Server) writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	s.writeRPC(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *Server) writeEncodedToolResult(w http.ResponseWriter, id json.RawMessage, wire []byte) {
	if len(wire) > s.maxResponseBytes || s.containsCredential(wire) {
		s.writeRPCError(w, http.StatusOK, id, -32603, "response exceeds bound")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(wire)
}

func (s *Server) writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	if id == nil {
		id = json.RawMessage("null")
	}
	s.writeRPC(w, status, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func (s *Server) writeRPC(w http.ResponseWriter, status int, response any) {
	wire, err := json.Marshal(response)
	if err != nil || len(wire) > s.maxResponseBytes || s.containsCredential(wire) {
		id := any(json.RawMessage("null"))
		if fields, ok := response.(map[string]any); ok {
			if original, exists := fields["id"]; exists {
				id = original
			}
		}
		wire, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32603, "message": "response exceeds bound"}})
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(wire)
}

func boundedBody(body io.ReadCloser, contentLength, limit int64) ([]byte, int, error) {
	defer body.Close()
	if contentLength > limit {
		return nil, http.StatusRequestEntityTooLarge, errors.New("oversized")
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if int64(len(data)) > limit {
		return nil, http.StatusRequestEntityTooLarge, errors.New("oversized")
	}
	return data, 0, nil
}

func firstJSONByte(raw []byte) byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0
	}
	return trimmed[0]
}

func canonicalRequestID(raw json.RawMessage) (json.RawMessage, string, bool) {
	if len(raw) == 0 || len(raw) > 256 {
		return nil, "", false
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, "", false
	}
	var canonical json.RawMessage
	switch id := value.(type) {
	case string:
		if id == "" || !utf8.ValidString(id) || len(id) > 256 {
			return nil, "", false
		}
		canonical, _ = json.Marshal(id)
	case json.Number:
		text := string(id)
		if len(text) > 64 || strings.ContainsAny(text, ".eE") {
			return nil, "", false
		}
		integer, ok := new(big.Int).SetString(text, 10)
		if !ok {
			return nil, "", false
		}
		canonical = json.RawMessage(integer.String())
	default:
		return nil, "", false
	}
	return canonical, string(canonical), true
}

func unambiguousJSON(raw json.RawMessage) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !uniqueJSONValue(decoder) {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}

func uniqueJSONValue(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return true
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return false
			}
			if _, duplicate := seen[key]; duplicate {
				return false
			}
			seen[key] = struct{}{}
			if !uniqueJSONValue(decoder) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim('}')
	case '[':
		for decoder.More() {
			if !uniqueJSONValue(decoder) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim(']')
	default:
		return false
	}
}

func decodeObject(raw json.RawMessage, destination any, required bool) error {
	if len(raw) == 0 {
		if required {
			return errors.New("missing object")
		}
		return nil
	}
	if firstJSONByte(raw) != '{' {
		return errors.New("object required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}

func decodeObjectDefault(raw json.RawMessage, destination any) error {
	if len(raw) == 0 {
		return nil
	}
	return decodeObject(raw, destination, true)
}

func objectOrAbsent(raw json.RawMessage) bool {
	return len(raw) == 0 || firstJSONByte(raw) == '{' && json.Valid(raw)
}

func emptyParams(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var value map[string]json.RawMessage
	return json.Unmarshal(raw, &value) == nil && len(value) == 0
}

func accepts(values []string, wanted string) bool {
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(item))
			if err == nil && mediaType == wanted {
				return true
			}
		}
	}
	return false
}

func validToolError(value ToolError) bool {
	return value.Code != "" && len(value.Code) <= 64 && toolNamePattern.MatchString(value.Code) && value.Message != "" && len(value.Message) <= 1024 && utf8.ValidString(value.Message)
}
