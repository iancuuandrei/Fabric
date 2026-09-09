package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"harness.local/engorch/internal/canonical"
)

type probeOutput struct {
	mu   sync.Mutex
	data []byte
}

func (p *probeOutput) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(b)
	remaining := (64 << 10) - len(p.data)
	if len(b) > remaining {
		b = b[:remaining]
	}
	p.data = append(p.data, b...)
	return n, nil
}

func (p *probeOutput) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.ReplaceAll(string(p.data), "fixture-server-secret", "[redacted]")
}

func TestPinnedExecutableVersionProbe(t *testing.T) {
	binary := os.Getenv("ENGORCH_OPENCODE_PROBE_BINARY")
	if binary == "" {
		t.Skip("explicit OpenCode binary probe opt-in required")
	}
	expected := os.Getenv("ENGORCH_OPENCODE_PROBE_SHA256")
	version := os.Getenv("ENGORCH_OPENCODE_PROBE_VERSION")
	if !filepath.IsAbs(binary) || len(expected) != 64 || version == "" {
		t.Fatal("binary identity and version required")
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != expected {
		t.Fatal("binary hash differs from admitted fixture")
	}
	root := t.TempDir()
	for _, name := range []string{"home", "config", "data", "cache", "state", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	env, err := HostEnvironment(root, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version")
	cmd.Env = env
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal("version probe failed", err)
	}
	if len(out) > 4096 || strings.TrimSpace(string(out)) != version {
		t.Fatal("version output differs from pin")
	}
}

func TestPinnedServerConfigurationProbe(t *testing.T) {
	binary := os.Getenv("ENGORCH_OPENCODE_PROBE_BINARY")
	if binary == "" {
		t.Skip("explicit OpenCode probe opt-in required")
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if !filepath.IsAbs(binary) || hex.EncodeToString(sum[:]) != os.Getenv("ENGORCH_OPENCODE_PROBE_SHA256") {
		t.Fatal("binary pin mismatch")
	}
	root := t.TempDir()
	// Loading this project config would enable sharing and attempt to load a
	// deliberately nonexistent local plugin. Neither is permitted by the host.
	if err := os.WriteFile(filepath.Join(root, "opencode.json"), []byte(`{"share":"auto","plugin":["./missing-fixture-plugin.ts"],"permission":"allow"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"home", "config", "data", "cache", "state", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_, port, _ := net.SplitHostPort(address)
	_ = listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output := &probeOutput{}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	process, admitted, startupEvidence, err := startReadyLimitedProcess(ctx, binary, os.Getenv("ENGORCH_OPENCODE_PROBE_SHA256"), root, portNumber, "fixture", "fixture-server-secret", output, 128, 2, 14*time.Second, os.Getenv("ENGORCH_OPENCODE_PROBE_DEBUG") == "1")
	if err != nil {
		t.Fatalf("bounded server readiness failed: %v; evidence: %+v; output: %s", err, startupEvidence, output.String())
	}
	defer func() {
		_ = process.Close()
		select {
		case <-process.Done():
		default:
			t.Error("owned process was not reaped")
		}
	}()
	if len(startupEvidence) < 1 || len(startupEvidence) > 2 || startupEvidence[len(startupEvidence)-1].Outcome != "ready" || startupEvidence[len(startupEvidence)-1].Configuration != admitted {
		t.Fatal("startup evidence does not bind admitted host")
	}
	for _, attempt := range startupEvidence[:len(startupEvidence)-1] {
		t.Logf("reaped failed startup attempt %d: %s: %v", attempt.Number, attempt.Outcome, attempt.Err)
	}
	// Check the parent-side launch environment independently of server readiness.
	limits := 0
	for _, entry := range process.command.Env {
		if strings.HasPrefix(entry, "OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX=") {
			limits++
			if entry != "OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX=128" {
				t.Fatal("output limit changed")
			}
		}
	}
	if limits != 1 {
		t.Fatal("output limit missing or duplicated")
	}
	client, err := NewClient("http://"+address, "fixture", "fixture-server-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	configBytes, err := client.read(ctx, "/config")
	if err != nil {
		t.Fatal("admitted server configuration became unavailable", err)
	}
	config, err := wireObject(configBytes)
	if err != nil {
		t.Fatal(err)
	}
	if reread, err := decodeHostConfiguration(configBytes); err != nil || reread.SHA256 != admitted.SHA256 {
		t.Fatal("strict host configuration admission", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/config", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.http.Do(req)
	if err != nil {
		t.Fatal("unauthenticated config probe failed")
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("server accepted unauthenticated config access")
	}
	var share string
	if field(config, "share", &share) != nil || share != "disabled" {
		t.Fatal("server sharing not disabled")
	}
	var plugins []json.RawMessage
	if json.Unmarshal(config["plugin"], &plugins) != nil || len(plugins) != 0 {
		t.Fatal("server loaded configured plugins")
	}
	var permission map[string]string
	if json.Unmarshal(config["permission"], &permission) != nil || permission["*"] != "deny" {
		t.Fatal("server permission ceiling not observed")
	}
	projectRaw, err := client.read(ctx, "/project/current")
	if err != nil {
		t.Fatal("read isolated project", err)
	}
	project, err := wireObject(projectRaw)
	if err != nil {
		t.Fatal(err)
	}
	var projectID string
	if field(project, "id", &projectID) != nil || projectID == "" {
		t.Fatal("project identity missing")
	}
	binding := SessionBinding{IntentID: strings.Repeat("a", 64), ProjectID: projectID, Directory: root, Agent: "build", Provider: "fixture", Model: "model"}
	sessionJournal := filepath.Join(root, "session-creation.jsonl")
	id, err := client.CreateSession(ctx, sessionJournal, binding)
	if err != nil {
		t.Fatal("isolated session creation/readback", err)
	}
	recovered, err := client.ReconcileSession(ctx, binding)
	if err != nil || recovered != id {
		t.Fatal("isolated session recovery", err)
	}
	if cached, err := client.RecoverSession(ctx, sessionJournal, binding); err != nil || cached != id {
		t.Fatal("durable session recovery", err)
	}
	// noReply stores a user message without invoking the fixture model. This
	// probes the real persistence schema, not model access or turn completion.
	body, err := canonical.Bytes(map[string]any{
		"messageID": "msg_engorchfixture", "agent": "build", "noReply": true, "variant": "low",
		"model": map[string]string{"providerID": "fixture", "modelID": "model"},
		"parts": []map[string]string{{"type": "text", "text": "local persistence probe"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.request(ctx, http.MethodPost, "/session/"+id+"/message", body); err != nil {
		t.Fatal("persist no-reply prompt", err)
	}
	messageBinding := Binding{SessionID: id, ParentID: "msg_engorchfixture", Provider: "fixture", Model: "model", Agent: "build", Directory: root, Root: root, Variant: "low"}
	if err := client.ReadPrompt(ctx, messageBinding, "local persistence probe"); err != nil {
		t.Fatal("real prompt readback", err)
	}
	transcript, err := client.ReadTranscript(ctx, id)
	if err != nil || len(transcript) != 1 || transcript[0].ID != messageBinding.ParentID || transcript[0].Role != "user" {
		t.Fatal("real session transcript", err)
	}
}
