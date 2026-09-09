package draftpr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestReadOnlyReconciliation(t *testing.T) {
	for _, scenario := range []string{"found", "absent", "ambiguous", "duplicate-page", "changed", "malformed-page", "bound", "substituted-number"} {
		t.Run(scenario, func(t *testing.T) {
			plan := fixturePlan(t)
			payload, _ := plan.Request()
			branch := func(ref, sha string) any {
				return map[string]any{"ref": ref, "sha": sha, "repo": map[string]any{"full_name": plan.Repository}}
			}
			full := map[string]any{"number": 7, "html_url": "https://github.com/fixture/project/pull/7", "state": "open", "draft": true, "title": payload.Title, "body": payload.Body, "head": branch(payload.Head, plan.Push.Candidate.Head), "base": branch(payload.Base, plan.BaseCommit)}
			c, err := NewClient("fixture")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			reads, lists := 0, 0
			c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" {
					t.Fatal("reconciliation mutated host")
				}
				var response any
				if req.URL.Path == "/repos/fixture/project/pulls/7" {
					reads++
					if scenario == "changed" {
						full["draft"] = false
					}
					if scenario == "substituted-number" {
						full["number"] = 8
						full["html_url"] = "https://github.com/fixture/project/pull/8"
					}
					response = full
				} else {
					lists++
					q := req.URL.Query()
					if req.URL.Path != "/repos/fixture/project/pulls" || q.Get("state") != "all" || q.Get("sort") != "created" || q.Get("direction") != "asc" || q.Get("per_page") != "100" || q.Get("page") != strconv.Itoa(lists) {
						t.Fatal("wrong pagination scope")
					}
					rows := []any{}
					if lists == 1 {
						rows = append(rows, map[string]any{"number": 1, "body": nil})
					}
					if lists == 2 && scenario != "absent" {
						rows = append(rows, map[string]any{"number": 7, "body": payload.Body})
					}
					if lists == 2 && scenario == "duplicate-page" {
						rows = append(rows, map[string]any{"number": 1, "body": nil})
					}
					if lists == 3 && scenario == "ambiguous" {
						rows = append(rows, map[string]any{"number": 8, "body": payload.Body})
					}
					if scenario == "bound" {
						rows = []any{map[string]any{"number": lists, "body": nil}}
					}
					response = rows
					if scenario == "malformed-page" {
						response = nil
					}
				}
				raw, _ := json.Marshal(response)
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw))), ContentLength: int64(len(raw)), Request: req}, nil
			})
			o, err := c.Reconcile(context.Background(), plan)
			if scenario == "found" {
				if err != nil || o.Number != 7 || reads != 1 || lists != 3 {
					t.Fatalf("reconciliation failed: %v", err)
				}
			} else if err == nil || o != (Observation{}) {
				t.Fatal("uncertain state confirmed")
			}
			if scenario == "bound" && lists != 21 {
				t.Fatal("pagination bound not enforced")
			}
			if scenario != "found" && scenario != "changed" && scenario != "substituted-number" && reads != 0 {
				t.Fatal("readback before unambiguous complete scan")
			}
		})
	}
}

func TestListPageRejectsIncompleteAndAmbiguousJSON(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `[{"number":1}]`, `[{"number":0,"body":null}]`, `[{"number":1,"body":4}]`, `[{"number":1,"body":null,"body":"x"}]`, `[{"number":1,"body":"\ud800"}]`} {
		if _, err := decodePage([]byte(raw)); err == nil {
			t.Fatal("invalid page admitted")
		}
	}
}
