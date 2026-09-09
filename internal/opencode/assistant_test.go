package opencode

import (
	"strings"
	"testing"
)

func TestAssistantFinalBinding(t *testing.T) {
	b := Binding{SessionID: "ses_fixture", ParentID: "msg_user", Provider: "fixture", Model: "model", Agent: "build", Directory: "/candidate", Root: "/candidate"}
	raw := `{"id":"msg_assistant","sessionID":"ses_fixture","parentID":"msg_user","providerID":"fixture","modelID":"model","agent":"build","role":"assistant","finish":"stop","time":{"created":10,"completed":20},"path":{"cwd":"/candidate","root":"/candidate"},"cost":0.0123,"tokens":{"input":10,"output":5,"reasoning":2,"cache":{"read":3,"write":1}}}`
	a, err := DecodeAssistant([]byte(raw), b)
	if err != nil || a.InputTokens != 10 || a.Binding != b {
		t.Fatal("valid final rejected", err)
	}
	for _, pair := range [][2]string{
		{`"stop"`, `"tool-calls"`}, {`"stop"`, `"length"`}, {`"stop"`, `"unknown"`},
		{`"msg_user"`, `"other"`}, {`"ses_fixture"`, `"other"`}, {`"model"`, `"other"`},
		{`"completed":20`, `"completed":9`}, {`"completed":20`, `"other":20`},
		{`"input":10`, `"input":10.5`}, {`"input":10`, `"input":-1`},
		{`"cost":0.0123`, `"cost":0.0123,"error":null`},
		{`"cost":0.0123`, `"cost":0.0123,"summary":true`},
		{`"cost":0.0123`, `"cost":0.0123,"variant":"unrequested"`},
		{`"cwd":"/candidate"`, `"cwd":"/foreign"`},
	} {
		if got, err := DecodeAssistant([]byte(strings.Replace(raw, pair[0], pair[1], 1)), b); err == nil || got != (Assistant{}) {
			t.Fatal("invalid final admitted")
		}
	}
}
