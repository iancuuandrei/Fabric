package opencode

import (
	"strings"
	"testing"
)

func TestTextPartBindingAndOrder(t *testing.T) {
	a := Assistant{ID: "msg", Binding: Binding{SessionID: "ses"}}
	part := `{"id":"p1","sessionID":"ses","messageID":"msg","type":"text","text":"first","time":{"start":1,"end":2}}`
	second := strings.Replace(strings.Replace(part, `"p1"`, `"p2"`, 1), `"first"`, `" second"`, 1)
	out, err := DecodeTextParts([]byte("["+part+","+second+"]"), a)
	if err != nil || out != "first second" {
		t.Fatal("text order changed", err)
	}
	for _, bad := range []string{
		"[" + part + "," + part + "]", `null`, `[]`,
		"[" + strings.Replace(part, `"ses"`, `"foreign"`, 1) + "]",
		"[" + strings.Replace(part, `"msg"`, `"foreign"`, 1) + "]",
		"[" + strings.Replace(part, `"type":"text"`, `"type":"tool"`, 1) + "]",
		"[" + strings.Replace(part, `"type":"text"`, `"type":"reasoning"`, 1) + "]",
		"[" + strings.Replace(part, `"text":"first"`, `"text":"first","ignored":true`, 1) + "]",
		"[" + strings.Replace(part, `"end":2`, `"end":0`, 1) + "]",
		"[" + strings.Replace(part, `,"end":2`, "", 1) + "]",
	} {
		if out, err := DecodeTextParts([]byte(bad), a); err == nil || out != "" {
			t.Fatal("invalid text admitted")
		}
	}
}
