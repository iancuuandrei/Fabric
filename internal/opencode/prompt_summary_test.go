package opencode

import (
	"fmt"
	"testing"
)

func TestPromptSummaryAllowsOnlyEmptyDiffProjection(t *testing.T) {
	b := Binding{SessionID: "ses_fixture", ParentID: "msg_fixture", Provider: "fixture", Model: "model", Agent: "build", Directory: "/candidate", Root: "/candidate", Variant: "low"}
	for _, summary := range []string{`{"diffs":[]}`, `null`, `{}`, `{"diffs":null}`, `{"diffs":[{}]}`, `{"diffs":[],"body":"other"}`, `{"diffs":[],"title":"other"}`, `{"diffs":[],"diffs":[]}`} {
		raw := fmt.Sprintf(`{"info":{"id":"msg_fixture","sessionID":"ses_fixture","role":"user","agent":"build","summary":%s,"model":{"providerID":"fixture","modelID":"model","variant":"low"}},"parts":[{"id":"prt_fixture","sessionID":"ses_fixture","messageID":"msg_fixture","type":"text","text":"task"}]}`, summary)
		err := decodePrompt([]byte(raw), b, "task")
		if (err == nil) != (summary == `{"diffs":[]}`) {
			t.Fatalf("summary %s: %v", summary, err)
		}
	}
}
