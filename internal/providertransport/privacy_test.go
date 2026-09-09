package providertransport

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/providergateway"
)

func TestFiniteAdaptersRejectPrivacyDriftBeforeDispatch(t *testing.T) {
	for _, adapter := range []string{providergateway.OpenAIChatCompletionsAdapter, providergateway.OpenAIResponsesAdapter, providergateway.AnthropicMessagesAdapter} {
		for _, framing := range []providergateway.ResponseFraming{providergateway.ResponseFramingJSON, providergateway.ResponseFramingSSE} {
			for _, change := range []string{"training", "retention", "terms", "repository-class"} {
				t.Run(adapter+"/"+string(framing)+"/"+change, func(t *testing.T) {
					server := newTLSServer(t, func(http.ResponseWriter, *http.Request) { t.Error("privacy drift reached network") })
					protocol := finiteProtocolFixture(t, adapter)
					protocol.privacy = &access.PrivacyPolicy{Version: 1, Training: "excluded", Retention: "zero"}
					protocol.capabilities.SupportedResponseFramings = []providergateway.ResponseFraming{framing}
					protocol.expectation.ResponseFraming = framing
					if framing == providergateway.ResponseFramingSSE {
						protocol.body = []byte(strings.Replace(string(protocol.body), `"stream":false`, `"stream":true`, 1))
						if adapter == providergateway.OpenAIChatCompletionsAdapter {
							protocol.body = []byte(strings.Replace(string(protocol.body), `"stream":true`, `"stream":true,"stream_options":{"include_usage":true}`, 1))
						}
						protocol.expectation.MaxBytes = int64(len(protocol.body))
					}
					f := newTransportFixtureForProtocol(t, server, "subscription", "bearer", "", 16<<10, protocol)
					before, err := journal.Read(f.gatewayPath)
					if err != nil {
						t.Fatal(err)
					}
					request, err := freezeRequest(f.request)
					if err != nil {
						t.Fatal(err)
					}
					switch change {
					case "training":
						request.Policy.Profiles[0].Privacy.Training = "allowed"
					case "retention":
						request.Policy.Profiles[0].Privacy.Retention = "provider-defined"
					case "terms":
						request.Policy.Profiles[0].Privacy.TermsSHA256 = strings.Repeat("a", 64)
					case "repository-class":
						request.Policy.Class = access.Confidential
					}
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					result, err := f.client.Execute(ctx, request)
					if err == nil || result.Content != nil || len(result.Body) != 0 {
						t.Fatal("privacy drift accepted", err)
					}
					after, readErr := journal.Read(f.gatewayPath)
					if readErr != nil || !reflect.DeepEqual(before, after) {
						t.Fatal("privacy rejection changed gateway", readErr)
					}
					if activeErr := access.RequireActive(f.accessPath, f.policy, f.intent); activeErr != nil {
						t.Fatal("original reservation changed", activeErr)
					}
				})
			}
		}
	}
}
