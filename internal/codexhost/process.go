package codexhost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"harness.local/engorch/internal/codexrpc"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/safepath"
)

// Receipt records the configuration/feature/MCP admission subset read by audit.
// It does not attest every requested CLI control or the effective native tools.
// In particular agents.enabled is requested, not observed by this receipt.
type Receipt struct {
	LaunchID                    string          `json:"launch_id"`
	Server                      codexrpc.Server `json:"server"`
	DisabledFeatures            []string        `json:"disabled_features"`
	PID                         int             `json:"pid"`
	CapabilityAttestationID     string          `json:"capability_attestation_id,omitempty"`
	CapabilityAttestationSHA256 string          `json:"capability_attestation_sha256,omitempty"`
}

// Validate checks the binding and observed controls represented in this receipt.
// It validates evidence structure without re-reading mutable files.
func (r Receipt) Validate(l Launch) error {
	id, err := l.BindingID()
	if err != nil {
		return err
	}
	pairedAttestation := r.CapabilityAttestationID == "" && r.CapabilityAttestationSHA256 == ""
	if r.CapabilityAttestationID != "" && r.CapabilityAttestationSHA256 != "" {
		expected, bindErr := AttestationBindingID(r.CapabilityAttestationSHA256, id)
		pairedAttestation = bindErr == nil && expected == r.CapabilityAttestationID
	}
	if r.LaunchID != id || r.PID <= 0 || filepath.Clean(r.Server.CodexHome) != filepath.Join(l.Root, "home") || r.Server.UserAgent == "" || r.Server.PlatformFamily == "" || r.Server.PlatformOS == "" || !slices.Equal(r.DisabledFeatures, disabled) || !pairedAttestation {
		return errors.New("host receipt binding or control evidence mismatch")
	}
	return nil
}

// ValidateAttested validates the receipt and requires an admitted R17 or R19 decision whose attestation ID matches the receipt.
func (r Receipt) ValidateAttested(l Launch, decision CapabilityConfinementDecision) error {
	if err := r.Validate(l); err != nil {
		return err
	}
	if !decision.Admitted || (decision.Observed != CapabilityConfinementR17 && decision.Observed != CapabilityConfinementR19) || decision.AttestationID == "" || r.CapabilityAttestationID != decision.AttestationID {
		return errors.New("host receipt differs from capability attestation")
	}
	return nil
}

// Host owns the admitted stdio connection and its local process lifetime.
type Host struct {
	Client  *codexrpc.Client
	Receipt Receipt
}

type processStream struct {
	input   io.WriteCloser
	output  io.ReadCloser
	command *exec.Cmd
	once    sync.Once
}

// Read consumes server protocol bytes.
func (p *processStream) Read(b []byte) (int, error) { return p.output.Read(b) }

// Write sends client protocol bytes.
func (p *processStream) Write(b []byte) (int, error) { return p.input.Write(b) }

// Close releases pipes and reaps the owned root process once.
func (p *processStream) Close() error {
	var err error
	p.once.Do(func() {
		err = errors.Join(p.input.Close(), p.output.Close())
		_ = p.command.Process.Kill()
		_ = p.command.Wait()
	})
	return err
}

func environment(home string) []string {
	result := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "PATH", "SYSTEMROOT", "WINDIR", "TEMP", "TMP", "LOCALAPPDATA", "USERPROFILE", "HOME":
			result = append(result, entry)
		}
	}
	return append(result, "CODEX_HOME="+home)
}

// Start launches an owned server and admits it only after observed configuration
// checks. No login material is inherited and no model turn is dispatched.
func Start(ctx context.Context, l Launch) (host *Host, err error) {
	return StartWithToolHandler(ctx, l, nil)
}

// StartWithToolHandler installs the caller's scoped handler on an admitted host.
func StartWithToolHandler(ctx context.Context, l Launch, handler codexrpc.ToolHandler) (host *Host, err error) {
	return startWithToolHandlerAndConstraint(ctx, l, handler, "", "", "", runtime.Profile{}, nil, time.Time{})
}

func startWithToolHandlerAndConstraint(ctx context.Context, l Launch, handler codexrpc.ToolHandler, catalogSource, catalogHash, cliIdentity string, profile runtime.Profile, dynamicTools []any, expires time.Time) (host *Host, err error) {
	id, err := l.ID()
	if err != nil {
		return nil, err
	}
	args := launchArguments()
	if catalogSource != "" {
		catalogPath, copyErr := copyPinnedCatalog(l, catalogSource, catalogHash)
		if copyErr != nil {
			return nil, copyErr
		}
		quoted, marshalErr := json.Marshal(catalogPath)
		if marshalErr != nil {
			return nil, marshalErr
		}
		args = append(args, "-c", "model_catalog_json="+string(quoted))
	}
	cmd := exec.CommandContext(ctx, l.Binary, args...)
	cmd.Dir = filepath.Join(l.Root, "workspace")
	cmd.Env = environment(filepath.Join(l.Root, "home"))
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, err
	}
	client := codexrpc.NewWithToolHandler(&processStream{input: input, output: output, command: cmd}, handler)
	if catalogSource != "" {
		if err := client.ConstrainThread(profile, cmd.Dir, dynamicTools, expires); err != nil {
			_ = client.Close()
			return nil, err
		}
	}
	defer func() {
		if err != nil {
			_ = client.Close()
		}
	}()
	server, err := client.InitializeCapabilities(ctx, filepath.Join(l.Root, "home"), true)
	if err != nil {
		return nil, err
	}
	if cliIdentity != "" && server.UserAgent != cliIdentity {
		return nil, errors.New("attested Codex server identity mismatch")
	}
	if err := audit(ctx, client, cmd.Dir); err != nil {
		return nil, err
	}
	return &Host{Client: client, Receipt: Receipt{LaunchID: id, Server: server, DisabledFeatures: append([]string{}, disabled...), PID: cmd.Process.Pid}}, nil
}

func copyPinnedCatalog(l Launch, source, expectedHash string) (string, error) {
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return "", errors.New("invalid pinned catalog source")
	}
	sourceRoot, err := os.OpenRoot(filepath.Dir(source))
	if err != nil {
		return "", err
	}
	defer sourceRoot.Close()
	destination := filepath.Join(l.Root, "home", "model_catalog.json")
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = os.Remove(destination)
		}
	}()
	hash, _, _, exists, copyErr := safepath.CopyRegular(sourceRoot, filepath.Base(source), 4<<20, f)
	err = errors.Join(copyErr, f.Sync(), f.Close())
	if err != nil || !exists {
		return "", errors.Join(err, errors.New("pinned catalog unavailable"))
	}
	if hash == "" || expectedHash != "" && hash != expectedHash {
		return "", errors.New("pinned catalog hash mismatch")
	}
	succeeded = true
	return destination, nil
}

// These are requested controls, not evidence of the effective tool inventory.
// agents.enabled is a separate hard override of model-selected multi-agent mode.
func launchArguments() []string {
	args := []string{"app-server", "--stdio", "--strict-config", "-c", `approval_policy="never"`, "-c", `sandbox_mode="read-only"`, "-c", `web_search="disabled"`, "-c", "agents.enabled=false"}
	for _, name := range disabled {
		args = append(args, "-c", "features."+name+"=false")
	}
	return append(args, "-c", "features.skip_host_skill_discovery=true")
}

func audit(ctx context.Context, c *codexrpc.Client, directory string) error {
	r, events, err := c.Call(ctx, "config/read", map[string]any{"cwd": directory, "includeLayers": false})
	if err != nil {
		return err
	}
	if len(r.Error) != 0 {
		return errors.New("host configuration readback rejected")
	}
	if err := startupNotifications(events); err != nil {
		return err
	}
	var config struct {
		Config struct {
			Approval string `json:"approval_policy"`
			Sandbox  string `json:"sandbox_mode"`
			Web      string `json:"web_search"`
		} `json:"config"`
	}
	if err := json.Unmarshal(r.Result, &config); err != nil {
		return err
	}
	if config.Config.Approval != "never" || config.Config.Sandbox != "read-only" || config.Config.Web != "disabled" {
		return errors.New("host effective policy differs from requested policy")
	}
	seen := map[string]bool{}
	skipHostSkills := false
	cursor := ""
	cursors := map[string]bool{}
	for page := 0; page < 16; page++ {
		params := map[string]any{"limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		r, events, err = c.Call(ctx, "experimentalFeature/list", params)
		if err != nil {
			return err
		}
		if len(r.Error) != 0 {
			return errors.New("host feature readback rejected")
		}
		if err := startupNotifications(events); err != nil {
			return err
		}
		var features struct {
			Data []struct {
				Name    string `json:"name"`
				Enabled *bool  `json:"enabled"`
			} `json:"data"`
			Next *string `json:"nextCursor"`
		}
		if err := json.Unmarshal(r.Result, &features); err != nil {
			return err
		}
		for _, feature := range features.Data {
			if feature.Name == "" || feature.Enabled == nil {
				return errors.New("invalid host feature observation")
			}
			if feature.Name == "skip_host_skill_discovery" {
				if skipHostSkills || !*feature.Enabled {
					return errors.New("host skill discovery control is missing or duplicated")
				}
				skipHostSkills = true
			}
			for _, name := range disabled {
				if feature.Name == name {
					if seen[name] || *feature.Enabled {
						return errors.New("host exposes duplicate or enabled restricted feature: " + name)
					}
					seen[name] = true
				}
			}
		}
		if features.Next == nil {
			break
		}
		cursor = *features.Next
		if cursor == "" || cursors[cursor] || page == 15 {
			return errors.New("invalid or excessive feature pagination")
		}
		cursors[cursor] = true
	}
	for _, name := range disabled {
		if !seen[name] {
			return errors.New("restricted host feature not observed: " + name)
		}
	}
	if !skipHostSkills {
		return errors.New("host skill discovery control not observed")
	}
	r, events, err = c.Call(ctx, "mcpServerStatus/list", map[string]any{"limit": 100, "detail": "toolsAndAuthOnly"})
	if err != nil {
		return err
	}
	if len(r.Error) != 0 {
		return errors.New("MCP inventory unavailable")
	}
	if err := startupNotifications(events); err != nil {
		return err
	}
	var inventory struct {
		Data []json.RawMessage `json:"data"`
		Next *string           `json:"nextCursor"`
	}
	if err := json.Unmarshal(r.Result, &inventory); err != nil {
		return err
	}
	if inventory.Data == nil || len(inventory.Data) != 0 || inventory.Next != nil {
		return errors.New("host has unadmitted MCP servers")
	}
	return nil
}

func startupNotifications(events []codexrpc.Message) error {
	for _, event := range events {
		if len(event.ID) != 0 || event.Method != "remoteControl/status/changed" {
			return errors.New("unexpected host startup notification: " + event.Method)
		}
		var status struct {
			Status        string  `json:"status"`
			EnvironmentID *string `json:"environmentId"`
		}
		if err := json.Unmarshal(event.Params, &status); err != nil {
			return err
		}
		if status.Status != "disabled" || status.EnvironmentID != nil {
			return errors.New("host remote control is not disabled")
		}
	}
	return nil
}

// Close stops the owned root process and closes its pipes. Descendant confinement
// is not claimed; this host has not dispatched native tools during admission.
func (h *Host) Close() error {
	if h == nil || h.Client == nil {
		return nil
	}
	return h.Client.Close()
}
