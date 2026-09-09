package providertransport

import (
	"context"
	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/providergateway"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestUnlimitedAccountingRetainsTechnicalLimitAndSettlesTransport(t *testing.T) {
	for _, billing := range []string{"api", "subscription"} {
		t.Run(billing, func(t *testing.T) {
			var calls atomic.Int32
			server := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Write(validChatJSON())
			})
			protocol := finiteProtocolFixture(t, providergateway.OpenAIChatCompletionsAdapter)
			protocol.unlimited = true
			f := newTransportFixtureForProtocol(t, server, billing, "bearer", "", 4096, protocol)
			if !f.intent.Reservation.UnlimitedTokens || f.intent.Reservation.Tokens != 0 || f.binding.ReservedTokens != 0 || f.binding.ProviderReservation == nil || f.binding.ProviderReservation.Tokens != 25 {
				t.Fatal("accounting conflated with technical bound")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := f.client.Execute(ctx, f.request)
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || result.Receipt.Usage.InputTokens != 3 || result.Receipt.Usage.OutputTokens != 2 {
				t.Fatal("not one exact usage receipt")
			}
			routeID, _ := f.intent.Route.ID()
			input, output := result.Receipt.Usage.InputTokens, result.Receipt.Usage.OutputTokens
			receipt := access.Receipt{InvocationID: f.intent.Reservation.InvocationID, RouteID: routeID, Status: "completed", OutputHash: digestText("ok"), InputTokens: &input, OutputTokens: &output}
			if err := access.RecordTerminal(f.accessPath, f.policy, receipt); err != nil {
				t.Fatal(err)
			}
			if err := access.RequireTerminal(f.accessPath, f.policy, f.intent, receipt); err != nil {
				t.Fatal(err)
			}
			state, err := providergateway.Inspect(f.gatewayPath)
			if err != nil || !state.Finished || state.Pending != nil {
				t.Fatal("terminal replay failed", err)
			}
			// A modified technical declaration cannot authorize a second dispatch.
			bad := f.request
			changed := *bad.Binding.ProviderReservation
			changed.Tokens++
			bad.Binding.ProviderReservation = &changed
			if _, err := f.client.Execute(ctx, bad); err == nil || calls.Load() != 1 {
				t.Fatal("tampered technical bound admitted")
			}
		})
	}
}
