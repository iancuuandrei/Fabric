package contextmcp

import (
	"encoding/json"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/toolbridge"
)

func TestContentCeilingFitsEscapedReceiptEnvelope(t *testing.T) {
	for _, value := range []string{"<", ">", "&", "\u2028", "\\", "\x00", "\""} {
		content, err := canonical.Bytes(map[string]string{"value": strings.Repeat(value, MaxContentBytes)})
		if err != nil {
			t.Fatal(err)
		}
		// Find the largest whole-string fixture fitting the broker ceiling.
		count := MaxContentBytes
		for len(content) > MaxContentBytes {
			count = count * (MaxContentBytes - 16) / len(content)
			content, err = canonical.Bytes(map[string]string{"value": strings.Repeat(value, count)})
			if err != nil {
				t.Fatal(err)
			}
		}
		id := strings.Repeat("a", 64)
		receipt, err := canonical.Bytes(contextbroker.Response{Version: 1, BindingID: id, InvocationID: id, RequestID: id, CallID: id, Success: true, Content: content})
		if err != nil {
			t.Fatal(err)
		}
		requestID := json.RawMessage(`"` + strings.Repeat("<", 254) + `"`)
		wire, err := toolbridge.EncodeToolResult(requestID, toolbridge.Result{JSON: receipt})
		if err != nil || len(wire) > MaxWireResponseBytes {
			t.Fatalf("wire bound violated: %d %v", len(wire), err)
		}
	}
}
