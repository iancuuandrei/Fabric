package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/toolbridge"
	"harness.local/engorch/internal/toolreceipts"
)

func TestCompositeDispatchSinglePostAndOfflineRecovery(t *testing.T) {
	for _, lostResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "lost-response"}[lostResponse], func(t *testing.T) {
			turn, brokerPath, broker := synchronousToolFixture(t)
			defer broker.Close()
			var err error
			turn.Dispatch, err = DispatchForInvocation(turn.Invocation, "ses_tool", "msg_user", "build", t.TempDir(), "/")
			if err != nil {
				t.Fatal(err)
			}
			contextProjection, err := contextmcp.Projection(broker)
			if err != nil {
				t.Fatal(err)
			}
			owners := []toolreceipts.OwnedProjection{{Owner: toolreceipts.OwnerContext, Projection: contextProjection}, {Owner: toolreceipts.OwnerAgent, Projection: compositeFixtureProjection(t, "send_message", "agent", false)}}
			catalog, err := toolreceipts.CatalogSHA256(owners)
			if err != nil {
				t.Fatal(err)
			}
			receiptPath := filepath.Join(t.TempDir(), "receipts")
			recorder, err := toolreceipts.Open(toolreceipts.Config{Path: receiptPath, InvocationID: turn.Invocation.ID, CallerBindingSHA256: strings.Repeat("d", 64), CatalogSHA256: catalog, Owners: owners})
			if err != nil {
				t.Fatal(err)
			}
			intent := CompositeDispatchIntent{Version: 1, Turn: turn, BrokerPath: brokerPath, ReceiptPath: receiptPath, Receipts: recorder.Binding()}
			path := filepath.Join(t.TempDir(), "dispatch")
			intent.DispatchPath = path
			var posts, gets atomic.Int32
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			var transcript []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					gets.Add(1)
					writeSynchronousJSON(w, string(transcript))
					return
				}
				posts.Add(1)
				entered <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				events, err := journal.Read(path)
				if err != nil || len(events) != 1 || events[0].Kind != "opencode.composite-dispatch-intent.v1" {
					t.Error("POST lacks durable intent", err)
				}
				if lostResponse {
					http.Error(w, "fixture lost response", http.StatusBadGateway)
					return
				}
				var observations []toolreceipts.Observation
				for index, tool := range []string{"source_read", "send_message"} {
					call := toolbridge.Call{RequestID: json.RawMessage(string(rune('1' + index))), Tool: tool, Arguments: json.RawMessage(`{"path":"source.txt","offset":0,"limit":10}`)}
					if tool == "send_message" {
						call.Arguments = json.RawMessage(`{"body":"done"}`)
					}
					result, err := recorder.Projection().Call(r.Context(), call)
					if err != nil {
						t.Error(err)
						http.Error(w, "fixture callback failed", 500)
						return
					}
					observations = append(observations, toolreceipts.Observation{Call: call, Result: result})
				}
				rows := compositeTranscript(turn.Dispatch.Binding, observations)
				parts(rows[0])[0]["text"] = turn.Dispatch.Text
				rows[0]["info"].(map[string]any)["model"].(map[string]any)["variant"] = turn.Dispatch.Binding.Variant
				for _, row := range rows[1:] {
					row["info"].(map[string]any)["variant"] = turn.Dispatch.Binding.Variant
				}
				transcript = marshalToolTranscript(t, rows)
				response, marshalErr := json.Marshal(rows[len(rows)-1])
				if marshalErr != nil {
					t.Error(marshalErr)
					http.Error(w, "fixture response invalid", 500)
					return
				}
				writeSynchronousJSON(w, string(response))
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "fixture", "secret")
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			brokerState, err := contextbroker.Inspect(brokerPath)
			if err != nil || brokerState.Binding == nil {
				t.Fatal("broker binding unavailable", err)
			}
			verify := func(owner toolreceipts.Owner, call toolbridge.Call, result toolbridge.Result) error {
				if owner == toolreceipts.OwnerContext {
					return contextmcp.VerifyBackendReceipt(brokerPath, *brokerState.Binding, call, result)
				}
				expected, err := canonical.Bytes(map[string]any{"kind": "agent", "request_id": json.RawMessage(call.RequestID)})
				if err != nil || owner != toolreceipts.OwnerAgent || call.Tool != "send_message" || string(result.JSON) != string(expected) {
					return errors.New("agent fixture source differs")
				}
				return nil
			}
			type dispatchResult struct {
				observation CompositeToolTurnObservation
				err         error
			}
			finished := make(chan dispatchResult, 1)
			go func() {
				observation, err := client.SubmitCompositeToolTurn(ctx, path, intent, verify)
				finished <- dispatchResult{observation, err}
			}()
			select {
			case <-entered:
			case early := <-finished:
				t.Fatal("first dispatch never reached POST", early.err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if _, err := client.SubmitCompositeToolTurn(ctx, path, intent, verify); err == nil || posts.Load() != 1 {
				t.Fatal("concurrent duplicate POST admitted", err, posts.Load())
			}
			close(release)
			completed := <-finished
			observed, err := completed.observation, completed.err
			if lostResponse {
				if err == nil {
					t.Fatal("lost response completed")
				}
				if _, err := RecoverCompositeToolTurn(path, intent, verify); err == nil {
					t.Fatal("intent-only completed")
				}
			} else {
				if err != nil || len(observed.Calls) != 2 {
					t.Fatal("composite dispatch", err)
				}
				recovered, err := RecoverCompositeToolTurn(path, intent, verify)
				if err != nil || !equalCanonical(recovered, observed) {
					t.Fatal("offline recovery differs", err)
				}
				if _, err := RecoverCompositeToolTurn(path, intent, func(toolreceipts.Owner, toolbridge.Call, toolbridge.Result) error {
					return errors.New("source rejected")
				}); err == nil {
					t.Fatal("backend rejection bypassed")
				}
				if _, err := RecoverCompositeToolTurn(filepath.Join(t.TempDir(), "foreign"), intent, verify); err == nil {
					t.Fatal("foreign dispatch path accepted")
				}
			}
			if _, err := client.SubmitCompositeToolTurn(ctx, path, intent, verify); err == nil || posts.Load() != 1 {
				t.Fatal("POST repeated", err, posts.Load())
			}
			wantGets := int32(1)
			if lostResponse {
				wantGets = 0
			}
			if gets.Load() != wantGets {
				t.Fatal("recovery performed network read", gets.Load())
			}
		})
	}
}
