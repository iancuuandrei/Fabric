// Package directapi provides a lightweight, durable, zero-boilerplate HTTP
// runtime supporting Anthropic Messages, OpenAI Chat Completions, and
// OpenAI-compatible endpoints (OpenRouter, Ollama, vLLM, LiteLLM, etc.)
// across all Fabric roles (planner, explorer, writer, fixer, reviewer),
// with bounded read-only repository tools, Anthropic prompt caching, and
// pooled HTTP/2 keep-alive connections.
package directapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/runtimeutil"
)

const (
	// RuntimeName identifies the direct HTTP API runtime in Fabric configuration.
	RuntimeName = "direct-api"

	// DialectAnthropic targets Anthropic's /v1/messages API.
	DialectAnthropic = "anthropic"
	// DialectOpenAI targets OpenAI / OpenAI-compatible /v1/chat/completions APIs.
	DialectOpenAI = "openai"

	defaultTimeout         = 5 * time.Minute
	defaultMaxOutputTokens = 16384
	maxResponseBytes       = 4 << 20
)

// sharedHTTPClient reuses persistent TLS/HTTP2 connections across multi-turn
// and multi-role direct-api calls instead of paying per-request handshakes.
var sharedHTTPClient = &http.Client{
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

// Config binds one direct HTTP API endpoint and credential env var.
// The API key value is never stored in configuration or journals.
type Config struct {
	Dialect         string
	BaseURL         string
	APIKeyEnv       string
	WorkDir         string
	MaxToolTurns    int
	MaxOutputTokens int64
	Timeout         time.Duration
	JournalPath     string
	HTTPClient      *http.Client
}

// Intent records an admitted direct-API invocation before network I/O.
type Intent struct {
	Version      int             `json:"version"`
	InvocationID string          `json:"invocation_id"`
	Dialect      string          `json:"dialect"`
	BaseURL      string          `json:"base_url"`
	Profile      runtime.Profile `json:"profile"`
	InputHash    string          `json:"input_hash"`
}

// Receipt records the completed direct-API response bound to its intent.
type Receipt struct {
	Version      int            `json:"version"`
	InvocationID string         `json:"invocation_id"`
	RequestID    string         `json:"request_id,omitempty"`
	ToolTurns    int            `json:"tool_turns,omitempty"`
	ResultHash   string         `json:"result_hash"`
	Result       runtime.Result `json:"result"`
}

// State is the replayed durable state of one direct-API runtime journal.
type State struct {
	Intent  *Intent
	Receipt *Receipt
	Head    string
}

// Inspect replays a direct-API runtime journal without making network calls.
func Inspect(path string) (State, error) {
	in, rec, head, err := runtimeutil.InspectJournal[Intent, Receipt](path, "directapi.intent", "directapi.result")
	if err != nil {
		return State{}, err
	}
	return State{Intent: in, Receipt: rec, Head: head}, nil
}

// DefaultBaseURL returns the standard API base URL for a dialect.
func DefaultBaseURL(dialect string) string {
	switch dialect {
	case DialectAnthropic:
		return "https://api.anthropic.com"
	case DialectOpenAI:
		return "https://api.openai.com"
	default:
		return ""
	}
}

// DefaultAPIKeyEnv returns the standard API key environment variable for a dialect.
func DefaultAPIKeyEnv(dialect string) string {
	switch dialect {
	case DialectAnthropic:
		return "ANTHROPIC_API_KEY"
	case DialectOpenAI:
		return "OPENAI_API_KEY"
	default:
		return ""
	}
}

func validateEvents(events []journal.Event) error {
	return runtimeutil.ValidateTwoEventJournal(
		events,
		"direct-api",
		"directapi.intent",
		"directapi.result",
		func(in Intent) string { return in.InvocationID },
		func(rec Receipt) string { return rec.InvocationID },
	)
}

// Execute runs or deterministically recovers one bounded direct HTTP API turn
// (with optional bounded read-only repository tool turns when cfg.WorkDir is set).
func Execute(ctx context.Context, cfg Config, inv runtime.Invocation) (runtime.Result, string, error) {
	if err := inv.Validate(); err != nil {
		return runtime.Result{}, "", err
	}
	if inv.Profile.Runtime != RuntimeName {
		return runtime.Result{}, "", fmt.Errorf("direct-api runtime required, got %q", inv.Profile.Runtime)
	}
	dialect := strings.ToLower(strings.TrimSpace(cfg.Dialect))
	if dialect != DialectAnthropic && dialect != DialectOpenAI {
		return runtime.Result{}, "", fmt.Errorf("unsupported direct-api dialect %q (use anthropic or openai)", cfg.Dialect)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL(dialect)
	}
	if cfg.JournalPath == "" {
		return runtime.Result{}, "", errors.New("direct-api journal path is required")
	}

	wantIntent := Intent{
		Version:      1,
		InvocationID: inv.ID,
		Dialect:      dialect,
		BaseURL:      baseURL,
		Profile:      inv.Profile,
		InputHash:    runtimeutil.InputSHA256(inv.Input),
	}

	cached, head, err := runtimeutil.CheckReplay(
		cfg.JournalPath,
		"direct-api",
		"directapi.intent",
		"directapi.result",
		wantIntent,
		inv,
		func(rec Receipt) runtime.Result { return rec.Result },
	)
	if err != nil {
		return runtime.Result{}, "", err
	}
	if cached != nil {
		return *cached, head, nil
	}

	keyEnv := strings.TrimSpace(cfg.APIKeyEnv)
	if keyEnv == "" {
		keyEnv = DefaultAPIKeyEnv(dialect)
	}
	apiKey := os.Getenv(keyEnv)
	if apiKey == "" && !isLoopbackURL(baseURL) {
		return runtime.Result{}, "", fmt.Errorf("missing API key in environment variable %s", keyEnv)
	}

	tools, err := newRepoTools(cfg.WorkDir)
	if err != nil {
		return runtime.Result{}, "", err
	}

	if err := runtimeutil.AppendIntent(cfg.JournalPath, "directapi.intent", wantIntent, validateEvents); err != nil {
		return runtime.Result{}, "", err
	}

	maxTokens := cfg.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxOutputTokens
	}
	maxTurns := cfg.MaxToolTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxToolTurns
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := cfg.HTTPClient
	if client == nil {
		client = sharedHTTPClient
	}

	var res runtime.Result
	var reqID string
	var toolTurns int
	switch dialect {
	case DialectAnthropic:
		res, reqID, toolTurns, err = callAnthropicLoop(runCtx, client, baseURL, apiKey, maxTokens, maxTurns, tools, inv)
	case DialectOpenAI:
		res, reqID, toolTurns, err = callOpenAILoop(runCtx, client, baseURL, apiKey, maxTokens, maxTurns, tools, inv)
	}
	if err != nil {
		return runtime.Result{}, "", err
	}
	return runtimeutil.RecordResult(
		cfg.JournalPath,
		"directapi.result",
		"harness.directapi-result.v1",
		inv,
		res,
		validateEvents,
		func(resHash string) Receipt {
			return Receipt{
				Version:      1,
				InvocationID: inv.ID,
				RequestID:    reqID,
				ToolTurns:    toolTurns,
				ResultHash:   resHash,
				Result:       res,
			}
		},
	)
}

func roleSystemPrompt(role string, hasTools bool) string {
	toolHint := ""
	if hasTools {
		toolHint = " You may call read_file, list_files, and search_text to inspect repository source before producing your final answer; you have no write authority."
	}
	switch role {
	case "planner":
		return "You are Fabric's planning agent." + toolHint + " Return strictly the requested JSON schema without surrounding prose or markdown."
	case "explorer":
		return "You are Fabric's read-only exploration agent." + toolHint + " Return strictly the requested JSON object without surrounding prose."
	case "writer", "fixer":
		return "You are Fabric's scoped code writer." + toolHint + " Return strictly the requested file-proposal JSON contract matching exact file preimages and unique anchors."
	case "reviewer":
		return "You are Fabric's independent code reviewer." + toolHint + " Evaluate the candidate diff and checks with precision over recall and return strictly the requested review verdict JSON."
	default:
		return "Return strictly the requested JSON output for this Fabric engineering role." + toolHint
	}
}

func callAnthropicLoop(ctx context.Context, client *http.Client, baseURL, apiKey string, maxTokens int64, maxTurns int, tools *repoTools, inv runtime.Invocation) (runtime.Result, string, int, error) {
	messages := []map[string]any{
		{"role": "user", "content": inv.Input},
	}
	systemBlocks := []map[string]any{
		{
			"type":          "text",
			"text":          roleSystemPrompt(inv.Profile.Role, tools != nil),
			"cache_control": map[string]string{"type": "ephemeral"},
		},
	}

	var totalIn, totalOut int64
	var lastReqID string
	toolTurns := 0

	for turn := 0; turn <= maxTurns; turn++ {
		payload := map[string]any{
			"model":      inv.Profile.Model,
			"max_tokens": maxTokens,
			"system":     systemBlocks,
			"messages":   messages,
		}
		if tools != nil && turn < maxTurns {
			payload["tools"] = anthropicToolDefs()
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return runtime.Result{}, lastReqID, toolTurns, err
		}
		hdr := http.Header{
			"Content-Type":      {"application/json"},
			"anthropic-version": {"2023-06-01"},
		}
		if apiKey != "" {
			hdr.Set("x-api-key", apiKey)
		}
		statusCode, raw, respHdr, err := doDirectWithRetry(ctx, client, baseURL+"/v1/messages", hdr, body, 4)
		if err != nil {
			return runtime.Result{}, lastReqID, toolTurns, fmt.Errorf("anthropic request failed: %w", err)
		}
		if rid := respHdr.Get("request-id"); rid != "" {
			lastReqID = rid
		}
		if statusCode < 200 || statusCode >= 300 {
			return runtime.Result{}, lastReqID, toolTurns, fmt.Errorf("anthropic HTTP %d: %s", statusCode, runtimeutil.Truncate(string(raw), 512))
		}

		var decoded struct {
			ID         string `json:"id"`
			Model      string `json:"model"`
			StopReason string `json:"stop_reason"`
			Content    []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text,omitempty"`
				ID    string          `json:"id,omitempty"`
				Name  string          `json:"name,omitempty"`
				Input json.RawMessage `json:"input,omitempty"`
			} `json:"content"`
			Usage struct {
				InputTokens              int64 `json:"input_tokens"`
				CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
				OutputTokens             int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return runtime.Result{}, lastReqID, toolTurns, fmt.Errorf("decode anthropic response: %w", err)
		}
		if lastReqID == "" {
			lastReqID = decoded.ID
		}
		totalIn += decoded.Usage.InputTokens + decoded.Usage.CacheCreationInputTokens + decoded.Usage.CacheReadInputTokens
		totalOut += decoded.Usage.OutputTokens

		var toolUses []struct {
			ID    string
			Name  string
			Input json.RawMessage
		}
		var text strings.Builder
		for _, block := range decoded.Content {
			switch block.Type {
			case "text":
				text.WriteString(block.Text)
			case "tool_use":
				toolUses = append(toolUses, struct {
					ID    string
					Name  string
					Input json.RawMessage
				}{ID: block.ID, Name: block.Name, Input: block.Input})
			}
		}

		if len(toolUses) > 0 && tools != nil && turn < maxTurns {
			toolTurns++
			messages = append(messages, map[string]any{"role": "assistant", "content": decoded.Content})
			results := make([]map[string]any, 0, len(toolUses))
			for _, tu := range toolUses {
				results = append(results, map[string]any{
					"type":        "tool_result",
					"tool_use_id": tu.ID,
					"content":     tools.execute(tu.Name, tu.Input),
				})
			}
			messages = append(messages, map[string]any{"role": "user", "content": results})
			continue
		}

		out := runtimeutil.UnwrapCodeFence(text.String())
		if strings.TrimSpace(out) == "" {
			return runtime.Result{}, lastReqID, toolTurns, errors.New("anthropic returned empty text")
		}
		return runtimeutil.BuildResult(inv, out, runtime.Usage{InputTokens: &totalIn, OutputTokens: &totalOut}), lastReqID, toolTurns, nil
	}
	return runtime.Result{}, lastReqID, toolTurns, errors.New("anthropic exceeded bounded tool turn limit")
}

func callOpenAILoop(ctx context.Context, client *http.Client, baseURL, apiKey string, maxTokens int64, maxTurns int, tools *repoTools, inv runtime.Invocation) (runtime.Result, string, int, error) {
	messages := []map[string]any{
		{"role": "system", "content": roleSystemPrompt(inv.Profile.Role, tools != nil)},
		{"role": "user", "content": inv.Input},
	}
	endpoint := baseURL + "/v1/chat/completions"
	if strings.HasSuffix(baseURL, "/v1") {
		endpoint = baseURL + "/chat/completions"
	}

	var totalIn, totalOut int64
	var lastReqID string
	toolTurns := 0

	for turn := 0; turn <= maxTurns; turn++ {
		payload := map[string]any{
			"model":                 inv.Profile.Model,
			"max_completion_tokens": maxTokens,
			"messages":              messages,
		}
		if tools != nil && turn < maxTurns {
			payload["tools"] = openaiToolDefs()
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return runtime.Result{}, lastReqID, toolTurns, err
		}
		hdr := http.Header{"Content-Type": {"application/json"}}
		if apiKey != "" {
			hdr.Set("Authorization", "Bearer "+apiKey)
		}
		statusCode, raw, respHdr, err := doDirectWithRetry(ctx, client, endpoint, hdr, body, 4)
		if err != nil {
			return runtime.Result{}, lastReqID, toolTurns, fmt.Errorf("openai request failed: %w", err)
		}
		if rid := respHdr.Get("x-request-id"); rid != "" {
			lastReqID = rid
		}
		if statusCode < 200 || statusCode >= 300 {
			return runtime.Result{}, lastReqID, toolTurns, fmt.Errorf("openai HTTP %d: %s", statusCode, runtimeutil.Truncate(string(raw), 512))
		}

		var decoded struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Message struct {
					Role      string `json:"role"`
					Content   string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return runtime.Result{}, lastReqID, toolTurns, fmt.Errorf("decode openai response: %w", err)
		}
		if len(decoded.Choices) == 0 {
			return runtime.Result{}, lastReqID, toolTurns, errors.New("openai returned no choices")
		}
		if lastReqID == "" {
			lastReqID = decoded.ID
		}
		totalIn += decoded.Usage.PromptTokens
		totalOut += decoded.Usage.CompletionTokens

		msg := decoded.Choices[0].Message
		if len(msg.ToolCalls) > 0 && tools != nil && turn < maxTurns {
			toolTurns++
			messages = append(messages, map[string]any{
				"role":       "assistant",
				"content":    msg.Content,
				"tool_calls": msg.ToolCalls,
			})
			for _, tc := range msg.ToolCalls {
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": tc.ID,
					"content":      tools.execute(tc.Function.Name, json.RawMessage(tc.Function.Arguments)),
				})
			}
			continue
		}

		out := runtimeutil.UnwrapCodeFence(msg.Content)
		if strings.TrimSpace(out) == "" {
			return runtime.Result{}, lastReqID, toolTurns, errors.New("openai returned empty content")
		}
		return runtimeutil.BuildResult(inv, out, runtime.Usage{InputTokens: &totalIn, OutputTokens: &totalOut}), lastReqID, toolTurns, nil
	}
	return runtime.Result{}, lastReqID, toolTurns, errors.New("openai exceeded bounded tool turn limit")
}

func isLoopbackURL(u string) bool {
	lower := strings.ToLower(u)
	return strings.HasPrefix(lower, "http://127.0.0.1") || strings.HasPrefix(lower, "http://localhost") || strings.HasPrefix(lower, "http://[::1]")
}

func doDirectWithRetry(ctx context.Context, client *http.Client, endpoint string, hdr http.Header, body []byte, maxAttempts int) (int, []byte, http.Header, error) {
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	const baseDelay = 750 * time.Millisecond
	const maxDelay = 30 * time.Second
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return 0, nil, nil, err
		}
		req.Header = hdr.Clone()
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil || attempt == maxAttempts-1 || !isTransientNetErr(err) {
				return 0, nil, nil, err
			}
			if err := sleepWithCtx(ctx, backoffWithJitter(attempt, baseDelay, maxDelay, 0)); err != nil {
				return 0, nil, nil, err
			}
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if ctx.Err() != nil || attempt == maxAttempts-1 {
				return resp.StatusCode, raw, resp.Header, readErr
			}
			if err := sleepWithCtx(ctx, backoffWithJitter(attempt, baseDelay, maxDelay, 0)); err != nil {
				return 0, nil, nil, err
			}
			continue
		}
		if !isRetryableCode(resp.StatusCode, raw) || attempt == maxAttempts-1 {
			return resp.StatusCode, raw, resp.Header, nil
		}
		if err := sleepWithCtx(ctx, backoffWithJitter(attempt, baseDelay, maxDelay, retryAfterDelay(resp.Header))); err != nil {
			return 0, nil, nil, err
		}
	}
	return 0, nil, nil, lastErr
}

func isRetryableCode(code int, body []byte) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	case http.StatusTooManyRequests:
		low := bytes.ToLower(body)
		return !bytes.Contains(low, []byte("insufficient_quota")) && !bytes.Contains(low, []byte("credit balance is too low"))
	default:
		return false
	}
}

func isTransientNetErr(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection reset") || strings.Contains(msg, "broken pipe") || strings.Contains(msg, "http2:")
}

func retryAfterDelay(h http.Header) time.Duration {
	if msStr := strings.TrimSpace(h.Get("Retry-After-Ms")); msStr != "" {
		if ms, err := strconv.ParseFloat(msStr, 64); err == nil && ms > 0 {
			return time.Duration(ms * float64(time.Millisecond))
		}
	}
	if val := strings.TrimSpace(h.Get("Retry-After")); val != "" {
		if secs, err := strconv.ParseFloat(val, 64); err == nil && secs > 0 {
			return time.Duration(secs * float64(time.Second))
		}
	}
	return 0
}

func backoffWithJitter(attempt int, base, maxDelay, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return min(retryAfter, maxDelay)
	}
	exp := base << min(attempt, 8)
	if exp > maxDelay {
		exp = maxDelay
	}
	half := exp / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

func sleepWithCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
