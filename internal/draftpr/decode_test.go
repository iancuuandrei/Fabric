package draftpr

import (
	"encoding/json"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
)

func TestDecodeHostedResponse(t *testing.T) {
	p := fixturePlan(t)
	r, err := p.Request()
	if err != nil {
		t.Fatal(err)
	}
	branch := func(ref, sha string) map[string]any {
		return map[string]any{"ref": ref, "sha": sha, "repo": map[string]any{"full_name": p.Repository, "private": true}}
	}
	response := map[string]any{"number": 7, "html_url": "https://github.com/fixture/project/pull/7", "state": "open", "draft": true, "title": r.Title, "body": r.Body, "head": branch(r.Head, p.Push.Candidate.Head), "base": branch(r.Base, p.BaseCommit), "unused": []any{nil, "extra"}}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	o, err := DecodeObservation(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Validate(p); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"number", "html_url", "state", "draft", "title", "body", "head", "base"} {
		for _, mode := range []string{"missing", "null", "wrong-case"} {
			t.Run(key+"/"+mode, func(t *testing.T) {
				var m map[string]any
				if err := json.Unmarshal(raw, &m); err != nil {
					t.Fatal(err)
				}
				value := m[key]
				delete(m, key)
				if mode == "null" {
					m[key] = nil
				}
				if mode == "wrong-case" {
					m[strings.ToUpper(key)] = value
				}
				bad, _ := json.Marshal(m)
				if _, err := DecodeObservation(bad); err == nil {
					t.Fatal("partial response admitted")
				}
			})
		}
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"draft":true`, `"draft":true,"draft":false`, 1),
		strings.Replace(string(raw), `"private":true`, `"private":true,"private":false`, 1),
		strings.Replace(string(raw), `"draft":true`, `"draft":"true"`, 1),
		strings.Replace(string(raw), `"full_name":`, `"FULL_NAME":`, 1),
		strings.Replace(string(raw), `"ref":`, `"REF":`, 1),
		strings.Replace(string(raw), `"sha":`, `"SHA":`, 1),
		strings.Replace(string(raw), `"extra"`, `"\ud800"`, 1),
		string(raw) + `{}`, "null", "[]", string(raw[:len(raw)-1]), strings.Repeat(" ", canonical.MaxBytes+1),
	} {
		if _, err := DecodeObservation([]byte(bad)); err == nil {
			t.Fatal("ambiguous or malformed response admitted")
		}
	}
}
