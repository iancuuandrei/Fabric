package codexrpc

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type processStream struct {
	input   io.WriteCloser
	output  io.ReadCloser
	command *exec.Cmd
}

func (p *processStream) Read(b []byte) (int, error)  { return p.output.Read(b) }
func (p *processStream) Write(b []byte) (int, error) { return p.input.Write(b) }
func (p *processStream) Close() error {
	err := errors.Join(p.input.Close(), p.output.Close())
	_ = p.command.Process.Kill()
	_ = p.command.Wait()
	return err
}

func TestInstalledServerHandshake(t *testing.T) {
	binary := os.Getenv("ENGORCH_CODEX_PROBE_BINARY")
	if binary == "" {
		t.Skip("opt-in installed-server handshake; no model turn")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("probe requires absolute executable")
	}
	home, workspace := t.TempDir(), t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "app-server", "--stdio")
	cmd.Dir = workspace
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "PATH", "SYSTEMROOT", "WINDIR", "TEMP", "TMP", "LOCALAPPDATA", "USERPROFILE":
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "CODEX_HOME="+home)
	cmd.Stderr = io.Discard
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		t.Fatal(err)
	}
	c := New(&processStream{input, output, cmd})
	defer c.Close()
	server, err := c.Initialize(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("installed handshake PASS: %s; no thread or model turn dispatched", server.UserAgent)
}
