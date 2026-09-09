package providergateway

import (
	"strings"
	"testing"
)

func TestResponsesTerminalErrorExcludesArbitraryProviderStatus(t *testing.T) {
	for _, status := range []string{"private-provider-secret", "failed\nprivate-provider-secret", "", strings.Repeat("x", 4096)} {
		if got := (&ResponsesTerminalError{Status: status}).Error(); got != "provider Responses stream ended unsuccessfully" {
			t.Fatal("unrecognized provider status reached diagnostic")
		}
	}
	var absent *ResponsesTerminalError
	if absent.Error() != "provider Responses stream ended unsuccessfully" {
		t.Fatal("nil terminal differs")
	}
	for _, status := range []string{"failed", "incomplete", "error"} {
		if got := (&ResponsesTerminalError{Status: status}).Error(); !strings.HasSuffix(got, status) {
			t.Fatal("finite terminal lost")
		}
	}
}

func TestAnthropicTerminalErrorExcludesArbitraryProviderStatus(t *testing.T) {
	for _, status := range []string{"private-provider-secret", "refusal\nprivate-provider-secret", "", strings.Repeat("x", 4096)} {
		err := &AnthropicTerminalError{Status: status}
		if err.Error() != "provider Anthropic Messages stream ended unsuccessfully" {
			t.Fatal("unrecognized provider status reached diagnostic")
		}
	}
	var absent *AnthropicTerminalError
	if absent.Error() != "provider Anthropic Messages stream ended unsuccessfully" {
		t.Fatal("nil terminal differs")
	}
	if got := (&AnthropicTerminalError{Status: "refusal"}).Error(); !strings.HasSuffix(got, "refusal") {
		t.Fatal("finite refusal lost")
	}
}
