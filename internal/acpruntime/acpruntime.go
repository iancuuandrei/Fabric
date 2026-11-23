// Package acpruntime implements a thin Agent Client Protocol (ACP) JSON-RPC
// stdio adapter under Fabric's durable journal and authority boundaries.
// Any reverse filesystem-write or terminal-execution request from the agent is
// explicitly denied so Fabric retains sole write authority.
package acpruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/runtimeutil"
)

const (
	// RuntimeName identifies the ACP stdio runtime in Fabric configuration.
	RuntimeName = "acp-stdio"

	defaultTimeout = 10 * time.Minute
	maxOutputBytes = 4 << 20
)

// Config binds one pinned ACP agent executable.
type Config struct {
	Executable     string
	ExecutableHash string
	Args           []string
	WorkDir        string
	JournalPath    string
	Timeout        time.Duration
}

// Intent records an admitted ACP invocation before subprocess launch.
type Intent struct {
	Version        int             `json:"version"`
	InvocationID   string          `json:"invocation_id"`
	Executable     string          `json:"executable"`
	ExecutableHash string          `json:"executable_hash"`
	Profile        runtime.Profile `json:"profile"`
	InputHash      string          `json:"input_hash"`
}

// Receipt records the completed ACP result bound to the intent.
type Receipt struct {
	Version      int            `json:"version"`
	InvocationID string         `json:"invocation_id"`
	SessionID    string         `json:"session_id,omitempty"`
	StopReason   string         `json:"stop_reason,omitempty"`
	ResultHash   string         `json:"result_hash"`
	Result       runtime.Result `json:"result"`
}

// State is the replayed durable state of one ACP runtime journal.
type State struct {
	Intent  *Intent
	Receipt *Receipt
	Head    string
}

// Inspect replays an ACP runtime journal without launching any subprocess.
func Inspect(path string) (State, error) {
	in, rec, head, err := runtimeutil.InspectJournal[Intent, Receipt](path, "acp.intent", "acp.result")
	if err != nil {
		return State{}, err
	}
	return State{Intent: in, Receipt: rec, Head: head}, nil
}

// HashFile computes the lowercase SHA-256 hex digest of a regular file.
func HashFile(path string) (string, error) {
	return runtimeutil.HashFile(path)
}

func validateEvents(events []journal.Event) error {
	return runtimeutil.ValidateTwoEventJournal(
		events,
		"acp",
		"acp.intent",
		"acp.result",
		func(in Intent) string { return in.InvocationID },
		func(rec Receipt) string { return rec.InvocationID },
	)
}

// Execute runs or deterministically recovers one bounded ACP stdio turn.
func Execute(ctx context.Context, cfg Config, inv runtime.Invocation) (runtime.Result, string, error) {
	if err := inv.Validate(); err != nil {
		return runtime.Result{}, "", err
	}
	if inv.Profile.Runtime != RuntimeName {
		return runtime.Result{}, "", fmt.Errorf("acp-stdio runtime required, got %q", inv.Profile.Runtime)
	}
	if cfg.Executable == "" || cfg.JournalPath == "" {
		return runtime.Result{}, "", errors.New("acp-stdio executable and journal path are required")
	}
	if err := runtimeutil.VerifyExecutableHash(cfg.Executable, cfg.ExecutableHash, "acp"); err != nil {
		return runtime.Result{}, "", err
	}

	wantIntent := Intent{
		Version:        1,
		InvocationID:   inv.ID,
		Executable:     cfg.Executable,
		ExecutableHash: cfg.ExecutableHash,
		Profile:        inv.Profile,
		InputHash:      runtimeutil.InputSHA256(inv.Input),
	}

	cached, head, err := runtimeutil.PrepareInvocation(
		cfg.JournalPath,
		"acp runtime",
		"acp.intent",
		"acp.result",
		wantIntent,
		inv,
		validateEvents,
		func(rec Receipt) runtime.Result { return rec.Result },
	)
	if err != nil {
		return runtime.Result{}, "", err
	}
	if cached != nil {
		return *cached, head, nil
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	output, sessionID, stopReason, err := runACPSession(runCtx, cfg, inv)
	if err != nil {
		return runtime.Result{}, "", err
	}

	output = runtimeutil.UnwrapCodeFence(output)
	if strings.TrimSpace(output) == "" {
		return runtime.Result{}, "", errors.New("acp agent returned empty output")
	}
	res := runtimeutil.BuildResult(inv, output, runtime.Usage{})
	return runtimeutil.RecordResult(
		cfg.JournalPath,
		"acp.result",
		"harness.acp-result.v1",
		inv,
		res,
		validateEvents,
		func(resHash string) Receipt {
			return Receipt{
				Version:      1,
				InvocationID: inv.ID,
				SessionID:    sessionID,
				StopReason:   stopReason,
				ResultHash:   resHash,
				Result:       res,
			}
		},
	)
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func runACPSession(ctx context.Context, cfg Config, inv runtime.Invocation) (string, string, string, error) {
	cmd := exec.CommandContext(ctx, cfg.Executable, cfg.Args...)
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", "", "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", "", err
	}
	if err := cmd.Start(); err != nil {
		return "", "", "", fmt.Errorf("start acp agent: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	var writeMu sync.Mutex
	send := func(msg any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		b, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		_, err = stdin.Write(append(b, '\n'))
		return err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), maxOutputBytes)

	var textBuf strings.Builder
	call := func(id int, method string, params any) (json.RawMessage, error) {
		req := map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  method,
			"params":  params,
		}
		if err := send(req); err != nil {
			return nil, err
		}
		wantID := fmt.Sprintf("%d", id)
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var msg rpcMessage
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				continue
			}
			// Handle reverse agent->client requests by denying mutations.
			if msg.Method != "" && len(msg.ID) > 0 {
				_ = send(map[string]any{
					"jsonrpc": "2.0",
					"id":      msg.ID,
					"error":   map[string]any{"code": -32601, "message": "Fabric denies direct agent side effects"},
				})
				continue
			}
			// Handle streaming session/update notifications.
			if msg.Method == "session/update" && len(msg.Params) > 0 {
				collectSessionUpdate(&textBuf, msg.Params)
				continue
			}
			if strings.TrimSpace(string(msg.ID)) == wantID {
				if msg.Error != nil {
					return nil, fmt.Errorf("acp %s error %d: %s", method, msg.Error.Code, msg.Error.Message)
				}
				return msg.Result, nil
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("acp agent closed stdout before replying to %s", method)
	}

	// 1. initialize
	if _, err := call(1, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientInfo":      map[string]string{"name": "fabric", "version": "2.0.0"},
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
	}); err != nil {
		return "", "", "", err
	}

	// 2. session/new
	cwd := cfg.WorkDir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	sessRaw, err := call(2, "session/new", map[string]any{
		"cwd":        cwd,
		"mcpServers": []any{},
	})
	if err != nil {
		return "", "", "", err
	}
	var sessResp struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(sessRaw, &sessResp); err != nil || sessResp.SessionID == "" {
		return "", "", "", errors.New("acp session/new returned invalid sessionId")
	}

	// 3. session/prompt
	promptRaw, err := call(3, "session/prompt", map[string]any{
		"sessionId": sessResp.SessionID,
		"prompt": []map[string]any{
			{"type": "text", "text": inv.Input},
		},
	})
	if err != nil {
		return "", sessResp.SessionID, "", err
	}
	var promptResp struct {
		StopReason string `json:"stopReason"`
		Output     string `json:"output"`
	}
	_ = json.Unmarshal(promptRaw, &promptResp)
	out := textBuf.String()
	if strings.TrimSpace(out) == "" && strings.TrimSpace(promptResp.Output) != "" {
		out = promptResp.Output
	}
	return out, sessResp.SessionID, promptResp.StopReason, nil
}

func collectSessionUpdate(buf *strings.Builder, params json.RawMessage) {
	var envelope struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return
	}
	if envelope.Update.SessionUpdate == "agent_message_chunk" && envelope.Update.Content.Type == "text" {
		if buf.Len()+len(envelope.Update.Content.Text) <= maxOutputBytes {
			buf.WriteString(envelope.Update.Content.Text)
		}
	}
}
