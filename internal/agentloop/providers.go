package agentloop

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

	"github.com/iancuuandrei/fabric/internal/cost"
	"github.com/iancuuandrei/fabric/internal/directapi"
)

// Message represents one provider-neutral turn in the agentic conversation.
type Message struct {
	Role        string       `json:"role"` // "user", "assistant", "tool"
	Content     string       `json:"content,omitempty"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	ToolResults []ToolResult `json:"tool_results,omitempty"`
}

// ModelResponse is the parsed output of one LLM turn.
type ModelResponse struct {
	Text      string     `json:"text,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Usage     cost.Usage `json:"usage"`
	Stop      string     `json:"stop,omitempty"`
}

// Model executes one step of the agentic conversation.
type Model interface {
	Name() string
	Complete(ctx context.Context, system string, messages []Message, tools []ToolDef) (ModelResponse, error)
}

var pooledClient = &http.Client{
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

// HTTPModel adapts Anthropic Messages or OpenAI / OpenAI-compatible HTTP APIs
// into the agentloop Model interface with multi-turn Anthropic prompt caching
// and exponential-backoff retry on transient 429/5xx/529 responses.
type HTTPModel struct {
	Dialect         string
	BaseURL         string
	APIKeyEnv       string
	ModelName       string
	MaxOutputTokens int64
	MaxAttempts     int
	Client          *http.Client
}

// Name returns the configured model identifier.
func (m *HTTPModel) Name() string { return m.ModelName }

// Complete sends one conversation turn to Anthropic or OpenAI-compatible APIs.
func (m *HTTPModel) Complete(ctx context.Context, system string, messages []Message, tools []ToolDef) (ModelResponse, error) {
	dialect := strings.ToLower(strings.TrimSpace(m.Dialect))
	baseURL := strings.TrimRight(strings.TrimSpace(m.BaseURL), "/")
	if baseURL == "" {
		baseURL = directapi.DefaultBaseURL(dialect)
	}
	keyEnv := strings.TrimSpace(m.APIKeyEnv)
	if keyEnv == "" {
		keyEnv = directapi.DefaultAPIKeyEnv(dialect)
	}
	apiKey := os.Getenv(keyEnv)
	client := m.Client
	if client == nil {
		client = pooledClient
	}
	maxTok := m.MaxOutputTokens
	if maxTok <= 0 {
		maxTok = 16384
	}
	attempts := m.MaxAttempts
	if attempts <= 0 {
		attempts = 4
	}
	switch dialect {
	case directapi.DialectAnthropic:
		return m.completeAnthropic(ctx, client, baseURL, apiKey, maxTok, attempts, system, messages, tools)
	case directapi.DialectOpenAI:
		return m.completeOpenAI(ctx, client, baseURL, apiKey, maxTok, attempts, system, messages, tools)
	default:
		return ModelResponse{}, fmt.Errorf("unsupported HTTP model dialect %q", m.Dialect)
	}
}

func (m *HTTPModel) completeAnthropic(ctx context.Context, client *http.Client, baseURL, apiKey string, maxTok int64, attempts int, system string, messages []Message, tools []ToolDef) (ModelResponse, error) {
	wireMsgs := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			txt := msg.Content
			if txt == "" {
				txt = "(continue)"
			}
			wireMsgs = append(wireMsgs, map[string]any{
				"role":    "user",
				"content": []map[string]any{{"type": "text", "text": txt}},
			})
		case "assistant":
			var blocks []map[string]any
			if msg.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				var inObj any
				if err := json.Unmarshal(tc.Arguments, &inObj); err != nil || inObj == nil {
					inObj = map[string]any{}
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": inObj})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, map[string]any{"type": "text", "text": "(ok)"})
			}
			wireMsgs = append(wireMsgs, map[string]any{"role": "assistant", "content": blocks})
		case "tool":
			var blocks []map[string]any
			for _, tr := range msg.ToolResults {
				out := tr.Output
				if out == "" {
					out = "(empty output)"
				}
				blocks = append(blocks, map[string]any{
					"type":        "tool_result",
					"tool_use_id": tr.CallID,
					"content":     out,
					"is_error":    tr.IsError,
				})
			}
			wireMsgs = append(wireMsgs, map[string]any{"role": "user", "content": blocks})
		}
	}

	// Place up to 2 ephemeral cache breakpoints on the latest 2 user/tool turns
	// (plus 1 on system + 1 on last tool = exact Anthropic max of 4 breakpoints)
	// for ~90% input token cost savings across multi-turn trajectories.
	placed := 0
	for i := len(wireMsgs) - 1; i >= 0 && placed < 2; i-- {
		if wireMsgs[i]["role"] != "user" {
			continue
		}
		if blocks, ok := wireMsgs[i]["content"].([]map[string]any); ok && len(blocks) > 0 {
			blocks[len(blocks)-1]["cache_control"] = map[string]string{"type": "ephemeral"}
			placed++
		}
	}

	payload := map[string]any{
		"model":      m.ModelName,
		"max_tokens": maxTok,
		"system": []map[string]any{{
			"type":          "text",
			"text":          system,
			"cache_control": map[string]string{"type": "ephemeral"},
		}},
		"messages": wireMsgs,
	}
	if len(tools) > 0 {
		wireTools := make([]map[string]any, 0, len(tools))
		for i, td := range tools {
			entry := map[string]any{
				"name":         td.Name,
				"description":  td.Description,
				"input_schema": td.Parameters,
			}
			if i == len(tools)-1 {
				entry["cache_control"] = map[string]string{"type": "ephemeral"}
			}
			wireTools = append(wireTools, entry)
		}
		payload["tools"] = wireTools
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ModelResponse{}, err
	}
	hdr := http.Header{
		"Content-Type":      {"application/json"},
		"anthropic-version": {"2023-06-01"},
	}
	if apiKey != "" {
		hdr.Set("x-api-key", apiKey)
	}
	status, raw, err := doJSONWithRetry(ctx, client, baseURL+"/v1/messages", hdr, body, attempts)
	if err != nil {
		return ModelResponse{}, err
	}
	if status < 200 || status >= 300 {
		return ModelResponse{}, fmt.Errorf("anthropic HTTP %d: %s", status, string(raw[:min(len(raw), 512)]))
	}

	var dec struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &dec); err != nil {
		return ModelResponse{}, err
	}
	var out ModelResponse
	out.Stop = dec.StopReason
	out.Usage = cost.Usage{
		InputTokens:      dec.Usage.InputTokens + dec.Usage.CacheCreationInputTokens + dec.Usage.CacheReadInputTokens,
		CachedTokens:     dec.Usage.CacheReadInputTokens,
		CacheWriteTokens: dec.Usage.CacheCreationInputTokens,
		OutputTokens:     dec.Usage.OutputTokens,
	}
	var sb strings.Builder
	for _, blk := range dec.Content {
		switch blk.Type {
		case "text":
			sb.WriteString(blk.Text)
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: blk.ID, Name: blk.Name, Arguments: blk.Input})
		}
	}
	out.Text = sb.String()
	return out, nil
}

func (m *HTTPModel) completeOpenAI(ctx context.Context, client *http.Client, baseURL, apiKey string, maxTok int64, attempts int, system string, messages []Message, tools []ToolDef) (ModelResponse, error) {
	wireMsgs := []map[string]any{{"role": "system", "content": system}}
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			wireMsgs = append(wireMsgs, map[string]any{"role": "user", "content": msg.Content})
		case "assistant":
			entry := map[string]any{"role": "assistant", "content": msg.Content}
			if len(msg.ToolCalls) > 0 {
				tcs := make([]map[string]any, 0, len(msg.ToolCalls))
				for _, tc := range msg.ToolCalls {
					tcs = append(tcs, map[string]any{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]string{
							"name":      tc.Name,
							"arguments": string(tc.Arguments),
						},
					})
				}
				entry["tool_calls"] = tcs
			}
			wireMsgs = append(wireMsgs, entry)
		case "tool":
			for _, tr := range msg.ToolResults {
				content := tr.Output
				if content == "" {
					content = "(empty output)"
				}
				wireMsgs = append(wireMsgs, map[string]any{
					"role":         "tool",
					"tool_call_id": tr.CallID,
					"content":      content,
				})
			}
		}
	}

	payload := map[string]any{
		"model":                 m.ModelName,
		"max_completion_tokens": maxTok,
		"messages":              wireMsgs,
	}
	if len(tools) > 0 {
		wireTools := make([]map[string]any, 0, len(tools))
		for _, td := range tools {
			wireTools = append(wireTools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        td.Name,
					"description": td.Description,
					"parameters":  td.Parameters,
				},
			})
		}
		payload["tools"] = wireTools
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ModelResponse{}, err
	}
	endpoint := baseURL + "/v1/chat/completions"
	if strings.HasSuffix(baseURL, "/v1") {
		endpoint = baseURL + "/chat/completions"
	}
	hdr := http.Header{"Content-Type": {"application/json"}}
	if apiKey != "" {
		hdr.Set("Authorization", "Bearer "+apiKey)
	}
	status, raw, err := doJSONWithRetry(ctx, client, endpoint, hdr, body, attempts)
	if err != nil {
		return ModelResponse{}, err
	}
	if status < 200 || status >= 300 {
		return ModelResponse{}, fmt.Errorf("openai HTTP %d: %s", status, string(raw[:min(len(raw), 512)]))
	}

	var dec struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens        int64 `json:"prompt_tokens"`
			CompletionTokens    int64 `json:"completion_tokens"`
			PromptTokensDetails *struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &dec); err != nil {
		return ModelResponse{}, err
	}
	if len(dec.Choices) == 0 {
		return ModelResponse{}, errors.New("openai returned no choices")
	}
	var cached int64
	if dec.Usage.PromptTokensDetails != nil {
		cached = dec.Usage.PromptTokensDetails.CachedTokens
	}
	out := ModelResponse{
		Text: dec.Choices[0].Message.Content,
		Stop: dec.Choices[0].FinishReason,
		Usage: cost.Usage{
			InputTokens:  dec.Usage.PromptTokens,
			CachedTokens: cached,
			OutputTokens: dec.Usage.CompletionTokens,
		},
	}
	for _, tc := range dec.Choices[0].Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: json.RawMessage(tc.Function.Arguments),
		})
	}
	return out, nil
}

// doJSONWithRetry executes an HTTP POST with exponential backoff, jitter, and
// Retry-After handling for transient 408/429/5xx/529 and network errors while
// failing fast on hard quota exhaustion or context cancellation.
func doJSONWithRetry(ctx context.Context, client *http.Client, endpoint string, hdr http.Header, body []byte, maxAttempts int) (int, []byte, error) {
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	const baseDelay = 750 * time.Millisecond
	const maxDelay = 30 * time.Second

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		req.Header = hdr.Clone()
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil || !isRetryableNetError(err) || attempt == maxAttempts-1 {
				return 0, nil, err
			}
			if err := sleepCtx(ctx, retryBackoff(attempt, baseDelay, maxDelay, 0)); err != nil {
				return 0, nil, err
			}
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if ctx.Err() != nil || attempt == maxAttempts-1 {
				return resp.StatusCode, raw, readErr
			}
			if err := sleepCtx(ctx, retryBackoff(attempt, baseDelay, maxDelay, 0)); err != nil {
				return 0, nil, err
			}
			continue
		}
		if !isRetryableHTTPStatus(resp.StatusCode, raw) || attempt == maxAttempts-1 {
			return resp.StatusCode, raw, nil
		}
		delay := retryBackoff(attempt, baseDelay, maxDelay, parseRetryAfter(resp.Header))
		if err := sleepCtx(ctx, delay); err != nil {
			return 0, nil, err
		}
	}
	return 0, nil, lastErr
}

func isRetryableHTTPStatus(code int, body []byte) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	case http.StatusTooManyRequests:
		low := bytes.ToLower(body)
		if bytes.Contains(low, []byte("insufficient_quota")) || bytes.Contains(low, []byte("credit balance is too low")) {
			return false
		}
		return true
	default:
		return false
	}
}

func isRetryableNetError(err error) bool {
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

func parseRetryAfter(h http.Header) time.Duration {
	if msStr := strings.TrimSpace(h.Get("Retry-After-Ms")); msStr != "" {
		if ms, err := strconv.ParseFloat(msStr, 64); err == nil && ms > 0 {
			return time.Duration(ms * float64(time.Millisecond))
		}
	}
	val := strings.TrimSpace(h.Get("Retry-After"))
	if val == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(val, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(val); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func retryBackoff(attempt int, base, maxDelay, retryAfter time.Duration) time.Duration {
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

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
