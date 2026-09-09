package draftpr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
)

func TestAuthorizedCreationAndIndependentReadback(t *testing.T) {
	for _, scenario := range []string{"success", "no-approval", "changed-plan", "lost-response", "changed-readback", "readback-error", "changed-head", "changed-base", "substituted-number"} {
		t.Run(scenario, func(t *testing.T) {
			plan := fixturePlan(t)
			intent, err := plan.Intent()
			if err != nil {
				t.Fatal(err)
			}
			id, _ := intent.ID()
			auth := effects.Authorization{IntentID: id, Actor: "fixture"}
			payload, _ := plan.Request()
			branch := func(ref, sha string) any {
				return map[string]any{"ref": ref, "sha": sha, "repo": map[string]any{"full_name": plan.Repository}}
			}
			response := map[string]any{"number": 7, "html_url": "https://github.com/fixture/project/pull/7", "state": "open", "draft": true, "title": payload.Title, "body": payload.Body, "head": branch(payload.Head, plan.Push.Candidate.Head), "base": branch(payload.Base, plan.BaseCommit)}
			c, err := NewClient("fixture-token")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			posts, gets := 0, 0
			refs := 0
			c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.HasPrefix(req.URL.Path, "/repos/fixture/project/git/ref/") {
					refs++
					ref := strings.TrimPrefix(req.URL.Path, "/repos/fixture/project/git/ref/")
					sha := plan.Push.Candidate.Head
					if ref == strings.TrimPrefix(plan.BaseRef, "refs/") {
						sha = plan.BaseCommit
					} else if ref != strings.TrimPrefix(plan.Push.TargetRef, "refs/") {
						t.Fatal("unexpected ref request")
					}
					if scenario == "changed-head" && refs == 1 || scenario == "changed-base" && refs == 2 {
						sha = strings.Repeat("f", 40)
					}
					if req.Method != "GET" || posts != 0 {
						t.Fatal("preflight ordering mismatch")
					}
					raw, _ := json.Marshal(map[string]any{"ref": "refs/" + ref, "object": map[string]any{"type": "commit", "sha": sha}})
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw))), ContentLength: int64(len(raw)), Request: req}, nil
				}
				status := 200
				if req.Method == "POST" {
					if refs != 2 {
						t.Fatal("creation before branch preflight")
					}
					posts++
					status = 201
					if req.URL.Path != "/repos/fixture/project/pulls" {
						t.Fatal("wrong creation endpoint")
					}
					raw, err := io.ReadAll(req.Body)
					if err != nil {
						t.Fatal(err)
					}
					var sent CreateRequest
					if err := canonical.Decode(raw, &sent); err != nil || sent != payload {
						t.Fatal("creation body mismatch")
					}
					if scenario == "lost-response" {
						return nil, errors.New("connection lost after server mutation")
					}
				} else {
					gets++
					if req.Method != "GET" || req.URL.Path != "/repos/fixture/project/pulls/7" {
						t.Fatal("wrong readback endpoint")
					}
					if scenario == "changed-readback" {
						response["draft"] = false
					}
					if scenario == "substituted-number" {
						response["number"] = 8
						response["html_url"] = "https://github.com/fixture/project/pull/8"
					}
					if scenario == "readback-error" {
						return nil, errors.New("read unavailable")
					}
				}
				raw, _ := json.Marshal(response)
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw))), ContentLength: int64(len(raw)), Request: req}, nil
			})
			if scenario == "no-approval" {
				auth.IntentID = ""
			}
			if scenario == "changed-plan" {
				plan.Title += " changed"
			}
			o, err := c.Create(context.Background(), plan, intent, auth)
			if scenario == "success" {
				if err != nil || o.Number != 7 || posts != 1 || gets != 1 {
					t.Fatalf("creation not confirmed: %v", err)
				}
			} else {
				if err == nil || o != (Observation{}) {
					t.Fatal("uncertain result admitted")
				}
				if scenario == "no-approval" || scenario == "changed-plan" {
					if posts != 0 || gets != 0 || refs != 0 {
						t.Fatal("unauthorized request sent")
					}
				} else if scenario == "changed-head" || scenario == "changed-base" {
					if posts != 0 || gets != 0 {
						t.Fatal("creation after branch drift")
					}
				} else if posts != 1 {
					t.Fatal("creation retried")
				}
				if scenario == "lost-response" && gets != 0 {
					t.Fatal("invented readback locator")
				}
			}
		})
	}
}
