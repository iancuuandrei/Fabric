package opencode

import (
	"strings"
	"testing"
)

func TestSessionCreationMarkerBinding(t *testing.T) {
	id := strings.Repeat("a", 64)
	b := SessionBinding{IntentID: id, ProjectID: "project", Directory: "/candidate", Agent: "build", Provider: "fixture", Model: "model"}
	raw := `{"id":"ses_fixture","projectID":"project","directory":"/candidate","agent":"build","title":"engorch:` + id + `","metadata":{"engorch_intent_id":"` + id + `"},"model":{"id":"model","providerID":"fixture"}}`
	if got, err := DecodeSession([]byte(raw), b); err != nil || got != "ses_fixture" {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{
		{`"directory":"/candidate"`, `"directory":"/foreign"`},
		{`"projectID":"project"`, `"projectID":"other"`},
		{`"providerID":"fixture"`, `"providerID":"other"`},
		{`"engorch_intent_id"`, `"other_marker"`},
		{`"id":"ses_fixture"`, `"id":"ses_fixture","share":null`},
		{`"id":"ses_fixture"`, `"id":"ses_fixture","parentID":"ses_parent"`},
		{`"id":"ses_fixture"`, `"id":"ses_fixture","revert":{}`},
	} {
		if got, err := DecodeSession([]byte(strings.Replace(raw, pair[0], pair[1], 1)), b); err == nil || got != "" {
			t.Fatal("foreign/unsafe session admitted")
		}
	}
}
