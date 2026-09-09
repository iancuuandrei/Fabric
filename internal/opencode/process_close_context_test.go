package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const processCloseHelperDelay = "ENGORCH_PROCESS_CLOSE_HELPER_DELAY_MS"

func TestProcessCloseContextHelper(t *testing.T) {
	raw := os.Getenv(processCloseHelperDelay)
	if raw == "" {
		return
	}
	milliseconds, err := strconv.Atoi(raw)
	if err != nil || milliseconds < 1 {
		os.Exit(2)
	}
	time.Sleep(time.Duration(milliseconds) * time.Millisecond)
}

func TestProcessCloseContextReapsActualChild(t *testing.T) {
	process, cancellations := startProcessCloseHelper(t, 30*time.Second, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := process.CloseContext(ctx); err == nil {
		t.Fatal("canceled child reported successful exit")
	}
	select {
	case <-process.Done():
	default:
		t.Fatal("CloseContext returned before the child was reaped")
	}
	if cancellations.Load() != 1 {
		t.Fatal("process cancellation was not initiated exactly once")
	}
	if err := process.CloseContext(context.Background()); !errors.Is(err, process.err) && err != process.err {
		t.Fatal("terminal CloseContext did not return the process result", err)
	}
}

func TestProcessCloseContextDeadlineLeavesReapingUnresolved(t *testing.T) {
	process, cancellations := startProcessCloseHelper(t, 250*time.Millisecond, true)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := process.CloseContext(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("CloseContext did not report its deadline", err)
	}
	select {
	case <-process.Done():
		t.Fatal("deadline was reported after claiming the child was reaped")
	default:
	}
	if cancellations.Load() != 1 {
		t.Fatal("process cancellation was not initiated exactly once")
	}
	wait, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if err := process.CloseContext(wait); err == nil {
		t.Fatal("canceled process context was reported as a successful exit")
	}
	select {
	case <-process.Done():
	default:
		t.Fatal("later CloseContext did not observe eventual reap")
	}
	if cancellations.Load() != 1 {
		t.Fatal("later CloseContext repeated cancellation")
	}
}

func TestProcessIdentityBindsLaunchAndToolsAdmission(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	digestBytes := sha256.Sum256(raw)
	digest := hex.EncodeToString(digestBytes[:])
	root := t.TempDir()
	for _, name := range []string{"home", "config", "data", "cache", "state", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	spec := ToolsConfigurationSpec{
		Endpoint: "http://127.0.0.1:43123/mcp", Bearer: strings.Repeat("z", 40),
		ToolNames: []string{"source_list", "source_read"}, TimeoutMillis: 4321,
	}
	configuration, err := BuildToolsConfiguration(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".helper-config"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process, receipt, _, err := StartReadyLimitedToolsProcess(ctx, binary, digest, root, port, "identity-user", "identity-password", io.Discard, 42, 2, 750*time.Millisecond, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	identity, ok := process.identity()
	if !ok || identity.ExecutableSHA256 != digest || identity.Root != filepath.Clean(root) || identity.Endpoint != "http://127.0.0.1:"+strconv.Itoa(port) || identity.MaxOutputTokens != 42 || identity.Diagnostic {
		t.Fatal("immutable process launch identity mismatch", identity)
	}
	authDigest, _ := processServerAuthDigest("identity-user", "identity-password")
	bearerDigest, _ := toolsBearerDigest(spec.Bearer)
	if identity.AuthSHA256 != authDigest || identity.Tools == nil || identity.Tools.Endpoint != spec.Endpoint || identity.Tools.BearerSHA256 != bearerDigest || identity.Tools.TimeoutMillis != spec.TimeoutMillis || identity.AdmittedTools == nil || identity.AdmittedTools.SHA256 != receipt.SHA256 {
		t.Fatal("tool launch or admission identity mismatch", identity)
	}
	identity.Tools.ToolIDs[0] = "mutated"
	identity.AdmittedTools.ToolIDs[0] = "mutated"
	replayed, ok := process.identity()
	if !ok || replayed.Tools.ToolIDs[0] == "mutated" || replayed.AdmittedTools.ToolIDs[0] == "mutated" {
		t.Fatal("process identity accessor exposed mutable slices")
	}
}

func startProcessCloseHelper(t *testing.T, delay time.Duration, resistCancellation bool) (*Process, *atomic.Int32) {
	t.Helper()
	lifetime, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(lifetime, os.Args[0], "-test.run=^TestProcessCloseContextHelper$")
	command.Env = append(os.Environ(), processCloseHelperDelay+"="+strconv.FormatInt(delay.Milliseconds(), 10))
	if resistCancellation {
		command.Cancel = func() error { return nil }
		command.WaitDelay = 0
	}
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	process := &Process{command: command, done: make(chan struct{})}
	var cancellations atomic.Int32
	process.cancel = func() {
		cancellations.Add(1)
		cancel()
	}
	go func() {
		process.err = command.Wait()
		close(process.done)
	}()
	t.Cleanup(func() {
		_ = process.Close()
	})
	return process, &cancellations
}
