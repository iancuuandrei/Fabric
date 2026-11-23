package directapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestDirectAPIAnthropicAndOpenAIWithToolsCachingAndReplay(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\nfunc answer() int { return 42 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	anthTurns := 0
	oaiTurns := 0
	sawCacheControl := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var reqBody map[string]any
		_ = json.NewDecoder(r.Body).Decode(&reqBody)

		switch r.URL.Path {
		case "/v1/messages":
			anthTurns++
			if sys, ok := reqBody["system"].([]any); ok && len(sys) > 0 {
				if blk, ok := sys[0].(map[string]any); ok && blk["cache_control"] != nil {
					sawCacheControl = true
				}
			}
			if anthTurns == 1 {
				// Ask to read main.go AND attempt an illegal traversal ../secret.txt
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id":          "msg_1",
					"model":       "claude-sonnet-4-5",
					"stop_reason": "tool_use",
					"content": []map[string]any{
						{"type": "tool_use", "id": "tu_1", "name": "read_file", "input": map[string]any{"path": "main.go"}},
						{"type": "tool_use", "id": "tu_2", "name": "read_file", "input": map[string]any{"path": "../secret.txt"}},
					},
					"usage": map[string]int64{"input_tokens": 100, "cache_creation_input_tokens": 50, "output_tokens": 20},
				})
				return
			}
			// Verify tool results arrived cleanly and traversal was rejected.
			rawMsgs, _ := json.Marshal(reqBody["messages"])
			if !strings.Contains(string(rawMsgs), "return 42") || !strings.Contains(string(rawMsgs), "must stay inside repository workspace") {
				http.Error(w, "missing expected tool results: "+string(rawMsgs), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    "msg_2",
				"model": "claude-sonnet-4-5",
				"content": []map[string]string{
					{"type": "text", "text": "```json\n{\"verdict\":\"approve\",\"findings\":[]}\n```"},
				},
				"usage": map[string]int64{"input_tokens": 20, "cache_read_input_tokens": 150, "output_tokens": 15},
			})

		case "/v1/chat/completions":
			oaiTurns++
			if oaiTurns == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id":    "chatcmpl_1",
					"model": "gpt-5",
					"choices": []map[string]any{{
						"message": map[string]any{
							"role": "assistant",
							"tool_calls": []map[string]any{{
								"id":   "call_1",
								"type": "function",
								"function": map[string]string{
									"name":      "search_text",
									"arguments": `{"query":"answer"}`,
								},
							}},
						},
					}},
					"usage": map[string]int64{"prompt_tokens": 80, "completion_tokens": 12},
				})
				return
			}
			rawMsgs, _ := json.Marshal(reqBody["messages"])
			if !strings.Contains(string(rawMsgs), "main.go") {
				http.Error(w, "missing search_text result", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    "chatcmpl_2",
				"model": "gpt-5",
				"choices": []map[string]any{
					{"message": map[string]string{"role": "assistant", "content": `{"summary":"found answer in main.go"}`}},
				},
				"usage": map[string]int64{"prompt_tokens": 90, "completion_tokens": 18},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// 1. Anthropic multi-turn tool loop + prompt caching + replay.
	anthInv, err := runtime.NewInvocation(runtime.Profile{
		Runtime: RuntimeName, Provider: "anthropic", Model: "claude-sonnet-4-5", Effort: "medium", Role: "reviewer",
	}, "Review this candidate.")
	if err != nil {
		t.Fatal(err)
	}
	anthJournal := filepath.Join(t.TempDir(), "reviewer.directapi.jsonl")
	anthCfg := Config{Dialect: DialectAnthropic, BaseURL: srv.URL, WorkDir: workDir, JournalPath: anthJournal, HTTPClient: srv.Client()}
	res, head1, err := Execute(context.Background(), anthCfg, anthInv)
	if err != nil {
		t.Fatalf("anthropic Execute failed: %v", err)
	}
	if !sawCacheControl {
		t.Fatal("expected Anthropic cache_control ephemeral header block")
	}
	if anthTurns != 2 || res.Output != `{"verdict":"approve","findings":[]}` || *res.Usage.InputTokens != 320 || *res.Usage.OutputTokens != 35 {
		t.Fatalf("unexpected anthropic result: turns=%d res=%+v", anthTurns, res)
	}
	// Replay must not hit server again.
	res2, head2, err := Execute(context.Background(), anthCfg, anthInv)
	if err != nil || res2.Output != res.Output || head2 != head1 || anthTurns != 2 {
		t.Fatalf("expected journal replay without extra HTTP calls: turns=%d err=%v", anthTurns, err)
	}

	// 2. OpenAI multi-turn tool loop.
	oaiInv, err := runtime.NewInvocation(runtime.Profile{
		Runtime: RuntimeName, Provider: "openai", Model: "gpt-5", Effort: "medium", Role: "explorer",
	}, "Find answer().")
	if err != nil {
		t.Fatal(err)
	}
	oaiJournal := filepath.Join(t.TempDir(), "explorer.directapi.jsonl")
	oaiCfg := Config{Dialect: DialectOpenAI, BaseURL: srv.URL, WorkDir: workDir, JournalPath: oaiJournal, HTTPClient: srv.Client()}
	oaiRes, _, err := Execute(context.Background(), oaiCfg, oaiInv)
	if err != nil {
		t.Fatalf("openai Execute failed: %v", err)
	}
	if oaiTurns != 2 || oaiRes.Output != `{"summary":"found answer in main.go"}` || *oaiRes.Usage.InputTokens != 170 || *oaiRes.Usage.OutputTokens != 30 {
		t.Fatalf("unexpected openai result: turns=%d res=%+v", oaiTurns, oaiRes)
	}
}
