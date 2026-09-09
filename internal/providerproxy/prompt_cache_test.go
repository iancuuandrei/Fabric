package providerproxy

import (
	"encoding/json"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/providertransport"
)

func newResponsesPromptCacheFixture(t *testing.T, initial string) proxyFixture {
	t.Helper()
	capabilities, err := canonical.Bytes(providergateway.ResponsesModelCapabilities{
		Version: 1, SystemRoles: []string{"developer"}, TextFormats: []string{"plain"}, KnownExtensions: []string{"prompt_cache_key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	controls, err := json.Marshal(providergateway.ResponsesRequestExpectation{
		MaxOutputTokens: 5, StateMode: "full-input-stateless", SystemRole: "developer", PromptCacheKey: initial,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"wire-model","input":[{"role":"developer","content":"Policy."},{"role":"user","content":[{"type":"input_text","text":"Answer."}]}],"max_output_tokens":5,"stream":true,"store":false}`)
	return newProxyFixtureForAdapter(t, "https://api.example.test/v1/responses", providertransport.ClientOptions{}, providergateway.OpenAIResponsesAdapter, capabilities, providergateway.AdapterRequestExpectation{MaxBytes: 4096, MaxOutputTokens: 5, Controls: controls}, body)
}

func TestBindResponsesPromptCacheKeyUpdatesExactCapabilityOnce(t *testing.T) {
	fixture := newResponsesPromptCacheFixture(t, "")
	running, err := fixture.server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeProxy(t, running) })
	before, beforeID := running.Binding()
	if err := running.BindResponsesPromptCacheKey("ses_exact_123"); err != nil {
		t.Fatal(err)
	}
	after, afterID := running.Binding()
	if before.RequestExpectationID == after.RequestExpectationID || beforeID == afterID {
		t.Fatal("prompt cache key did not change exact proxy identities")
	}
	var controls providergateway.ResponsesRequestExpectation
	if err := json.Unmarshal(fixture.server.expectation.Controls, &controls); err != nil || controls.PromptCacheKey != "ses_exact_123" {
		t.Fatal("exact session identity was not frozen into Responses controls", controls, err)
	}
	if err := running.BindResponsesPromptCacheKey("ses_other"); err == nil {
		t.Fatal("prompt cache key was rebound")
	}
}

func TestBindResponsesPromptCacheKeyRejectsWrongOrStartedLifecycle(t *testing.T) {
	chat := newProxyFixture(t)
	chatRunning, err := chat.server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeProxy(t, chatRunning) })
	if err := chatRunning.BindResponsesPromptCacheKey("ses_exact"); err == nil {
		t.Fatal("Chat adapter admitted Responses cache binding")
	}

	for name, mutate := range map[string]func(*Server){
		"dispatched": func(s *Server) { s.dispatched = true },
		"active":     func(s *Server) { s.active = 1 },
		"terminal":   func(s *Server) { s.terminal = true },
		"poisoned":   func(s *Server) { s.poisoned = true },
		"closing":    func(s *Server) { s.closing = true },
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newResponsesPromptCacheFixture(t, "")
			running, err := fixture.server.Listen()
			if err != nil {
				t.Fatal(err)
			}
			fixture.server.mu.Lock()
			mutate(fixture.server)
			fixture.server.mu.Unlock()
			if err := running.BindResponsesPromptCacheKey("ses_exact"); err == nil {
				t.Fatal("lifecycle state admitted late cache binding")
			}
			fixture.server.mu.Lock()
			fixture.server.dispatched, fixture.server.active, fixture.server.terminal, fixture.server.poisoned, fixture.server.closing = false, 0, false, false, false
			fixture.server.mu.Unlock()
			closeProxy(t, running)
		})
	}

	preset := newResponsesPromptCacheFixture(t, "configured")
	presetRunning, err := preset.server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	if err := presetRunning.BindResponsesPromptCacheKey("ses_exact"); err == nil {
		t.Fatal("preconfigured prompt cache key was overwritten")
	}
	closeProxy(t, presetRunning)
}
