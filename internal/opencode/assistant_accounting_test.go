package opencode

import (
	"strings"
	"testing"
)

func TestAssistantRequiresObservedAccounting(t *testing.T) {
	b := Binding{SessionID: "ses_fixture", ParentID: "msg_user", Provider: "fixture", Model: "model", Agent: "build", Directory: "/candidate", Root: "/candidate"}
	raw := `{"id":"msg_assistant","sessionID":"ses_fixture","parentID":"msg_user","providerID":"fixture","modelID":"model","agent":"build","role":"assistant","finish":"stop","time":{"created":0,"completed":0},"path":{"cwd":"/candidate","root":"/candidate"},"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}}`
	if _, err := DecodeAssistant([]byte(raw), b); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"created", "completed", "input", "output", "reasoning", "read", "write"} {
		for _, replacement := range []string{`"` + key + `":null`, `"missing":0`, `"` + key + `":9007199254740992`} {
			changed := strings.Replace(raw, `"`+key+`":0`, replacement, 1)
			if got, err := DecodeAssistant([]byte(changed), b); err == nil || got != (Assistant{}) {
				t.Fatalf("unobserved or inexact %s accepted", key)
			}
		}
	}
}
