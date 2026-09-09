package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReadCurrentProjectAdmitsExactPinnedShape(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	sandbox := filepath.Join(filepath.Dir(root), "project-sandbox")
	raw := marshalProjectFixture(t, map[string]any{
		"id": "project_123", "worktree": root, "vcs": "git", "name": "fixture",
		"icon":      map[string]any{"url": "data:image/png;base64,AA==", "override": "fixture.svg", "color": "#123456"},
		"commands":  map[string]any{"start": "go test ./..."},
		"time":      map[string]any{"created": 1, "updated": 2, "initialized": 2},
		"sandboxes": []string{sandbox},
	})

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		user, password, authenticated := request.BasicAuth()
		if request.Method != http.MethodGet || request.URL.Path != "/project/current" || request.URL.Query().Get("directory") != root || len(request.URL.Query()) != 1 ||
			!authenticated || user != "fixture" || password != "secret" {
			t.Errorf("unexpected project request: method=%s path=%s query=%s auth=%t user=%q", request.Method, request.URL.Path, request.URL.RawQuery, authenticated, user)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write(raw)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	expected := ProjectExpectation{Directory: root, Mode: ProjectModeGit, Worktree: root}
	receipt, err := client.ReadCurrentProject(context.Background(), expected)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if receipt != (ProjectReceipt{
		SHA256: hex.EncodeToString(digest[:]), ID: "project_123", Directory: root,
		Mode: ProjectModeGit, Worktree: root, VCS: "git",
	}) {
		t.Fatalf("unexpected project receipt: %+v", receipt)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one read-only project request, got %d", calls.Load())
	}
}

func TestReadCurrentProjectRejectsInvalidExpectedRootBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	unclean := t.TempDir() + string(filepath.Separator) + ".." + string(filepath.Separator) + "unclean"
	for _, expected := range []ProjectExpectation{
		{Directory: "", Mode: ProjectModeGlobal},
		{Directory: "relative", Mode: ProjectModeGlobal},
		{Directory: unclean, Mode: ProjectModeGlobal},
		{Directory: filepath.Join(t.TempDir(), "literal%20name"), Mode: ProjectModeGlobal},
		{Directory: t.TempDir(), Mode: "unknown"},
		{Directory: t.TempDir(), Mode: ProjectModeGlobal, Worktree: t.TempDir()},
		{Directory: t.TempDir(), Mode: ProjectModeGit, Worktree: "relative"},
	} {
		if _, err := client.ReadCurrentProject(context.Background(), expected); err == nil {
			t.Fatalf("expected invalid project expectation %+v to fail", expected)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid expectation caused %d HTTP requests", calls.Load())
	}
}

func TestDecodeCurrentProjectRejectsSubstitutionAndMalformedPinnedFields(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	other := filepath.Join(filepath.Dir(root), "other-project")
	valid := func() map[string]any {
		return map[string]any{
			"id": "project_123", "worktree": root, "vcs": "git",
			"time": map[string]any{"created": 1, "updated": 2}, "sandboxes": []string{},
		}
	}
	expected := ProjectExpectation{Directory: root, Mode: ProjectModeGit, Worktree: root}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"foreign worktree", func(value map[string]any) { value["worktree"] = other }},
		{"expected root hidden as sandbox", func(value map[string]any) {
			value["worktree"] = other
			value["sandboxes"] = []string{root}
		}},
		{"invalid id", func(value map[string]any) { value["id"] = "project/id" }},
		{"unknown project field", func(value map[string]any) { value["extra"] = true }},
		{"unknown vcs", func(value map[string]any) { value["vcs"] = "svn" }},
		{"fractional time", func(value map[string]any) { value["time"] = map[string]any{"created": 1.5, "updated": 2} }},
		{"unknown time field", func(value map[string]any) { value["time"] = map[string]any{"created": 1, "updated": 2, "extra": 3} }},
		{"null sandboxes", func(value map[string]any) { value["sandboxes"] = nil }},
		{"relative sandbox", func(value map[string]any) { value["sandboxes"] = []string{"relative"} }},
		{"worktree repeated as sandbox", func(value map[string]any) { value["sandboxes"] = []string{root} }},
		{"duplicate sandbox", func(value map[string]any) { value["sandboxes"] = []string{other, other} }},
		{"wrong icon override type", func(value map[string]any) { value["icon"] = map[string]any{"override": true} }},
		{"unknown icon field", func(value map[string]any) { value["icon"] = map[string]any{"extra": "x"} }},
		{"unknown command field", func(value map[string]any) { value["commands"] = map[string]any{"stop": "x"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid()
			test.mutate(value)
			if _, err := decodeCurrentProject(marshalProjectFixture(t, value), expected); err == nil {
				t.Fatal("malformed project response was accepted")
			}
		})
	}

	duplicateID := `{"id":"project_123","id":"project_456","worktree":` + quoteProjectFixture(t, root) + `,"time":{"created":1,"updated":2},"sandboxes":[]}`
	if _, err := decodeCurrentProject([]byte(duplicateID), expected); err == nil {
		t.Fatal("ambiguous duplicate project identity was accepted")
	}
}

func TestDecodeCurrentProjectAcceptsMinimalPinnedShape(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	raw := marshalProjectFixture(t, map[string]any{
		"id": "global", "worktree": "/",
		"time": map[string]any{"created": 0, "updated": 0}, "sandboxes": []string{},
	})
	expected := ProjectExpectation{Directory: root, Mode: ProjectModeGlobal}
	receipt, err := decodeCurrentProject(raw, expected)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ID != "global" || receipt.Directory != root || receipt.Mode != ProjectModeGlobal || receipt.Worktree != "/" || receipt.VCS != "" || len(receipt.SHA256) != 64 {
		t.Fatalf("unexpected minimal project receipt: %+v", receipt)
	}
}

func TestDecodeCurrentProjectBindsExplicitModes(t *testing.T) {
	worktree := filepath.Clean(t.TempDir())
	directory := filepath.Join(filepath.Dir(worktree), "selected-sandbox")
	gitRaw := marshalProjectFixture(t, map[string]any{
		"id": "project_123", "worktree": worktree, "vcs": "git",
		"time": map[string]any{"created": 1, "updated": 2}, "sandboxes": []string{directory},
	})
	gitExpected := ProjectExpectation{Directory: directory, Mode: ProjectModeGit, Worktree: worktree}
	globalRaw := marshalProjectFixture(t, map[string]any{
		"id": "global", "worktree": "/",
		"time": map[string]any{"created": 1, "updated": 2}, "sandboxes": []string{},
	})
	if _, err := decodeCurrentProject(globalRaw, gitExpected); !errors.Is(err, ErrProjectGlobalIdentity) {
		t.Fatal("Git selector global identity did not retain its finite readiness class", err)
	}
	for name, value := range map[string]map[string]any{
		"foreign worktree": {"id": "global", "worktree": worktree, "time": map[string]any{"created": 1, "updated": 2}, "sandboxes": []string{}},
		"malformed time":   {"id": "global", "worktree": "/", "time": map[string]any{"created": "bad", "updated": 2}, "sandboxes": []string{}},
	} {
		if _, err := decodeCurrentProject(marshalProjectFixture(t, value), gitExpected); err == nil || errors.Is(err, ErrProjectGlobalIdentity) {
			t.Fatal("malformed global identity became retryable", name, err)
		}
	}
	if _, err := decodeCurrentProject(gitRaw, gitExpected); err != nil {
		t.Fatal("selected Git sandbox was not bound to its exact worktree:", err)
	}
	withoutSelectedDirectory := marshalProjectFixture(t, map[string]any{
		"id": "project_123", "worktree": worktree, "vcs": "git",
		"time": map[string]any{"created": 1, "updated": 2}, "sandboxes": []string{},
	})
	if _, err := decodeCurrentProject(withoutSelectedDirectory, gitExpected); err == nil {
		t.Fatal("Git response omitted the selected non-worktree directory")
	}

	globalExpected := ProjectExpectation{Directory: worktree, Mode: ProjectModeGlobal}
	for name, value := range map[string]map[string]any{
		"Git marker": {
			"id": "global", "worktree": "/", "vcs": "git",
			"time": map[string]any{"created": 1, "updated": 2}, "sandboxes": []string{},
		},
		"sandbox scope": {
			"id": "global", "worktree": "/",
			"time": map[string]any{"created": 1, "updated": 2}, "sandboxes": []string{directory},
		},
		"foreign global root": {
			"id": "global", "worktree": worktree,
			"time": map[string]any{"created": 1, "updated": 2}, "sandboxes": []string{},
		},
	} {
		t.Run("global rejects "+name, func(t *testing.T) {
			if _, err := decodeCurrentProject(marshalProjectFixture(t, value), globalExpected); err == nil {
				t.Fatal("global identity substitution was accepted")
			}
		})
	}
}

func TestDecodeCurrentProjectBoundsCollectionsAndText(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	tooMany := make([]string, projectMaxSandboxes+1)
	for i := range tooMany {
		tooMany[i] = filepath.Join(filepath.Dir(root), "sandbox-"+strings.Repeat("x", i%5)+string(rune('A'+i%26)))
	}
	value := map[string]any{
		"id": "project", "worktree": root, "name": strings.Repeat("x", projectMaxTextBytes+1),
		"time": map[string]any{"created": 1, "updated": 2}, "sandboxes": []string{},
	}
	expected := ProjectExpectation{Directory: root, Mode: ProjectModeGit, Worktree: root}
	if _, err := decodeCurrentProject(marshalProjectFixture(t, value), expected); err == nil {
		t.Fatal("oversized project text was accepted")
	}
	value["name"] = "bounded"
	value["sandboxes"] = tooMany
	if _, err := decodeCurrentProject(marshalProjectFixture(t, value), expected); err == nil {
		t.Fatal("oversized project sandbox set was accepted")
	}
}

func marshalProjectFixture(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func quoteProjectFixture(t *testing.T, value string) string {
	t.Helper()
	return string(marshalProjectFixture(t, value))
}
