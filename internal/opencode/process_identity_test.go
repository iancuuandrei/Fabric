package opencode

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestProcessIDRejectsMissingOwnedProcess(t *testing.T) {
	if _, err := (*Process)(nil).ProcessID(); err == nil {
		t.Fatal("nil process exposed an identifier")
	}
	if _, err := (&Process{}).ProcessID(); err == nil {
		t.Fatal("unstarted process exposed an identifier")
	}
}

func TestProcessIDReturnsExactStartedOwnedProcess(t *testing.T) {
	if os.Getenv("ENGORCH_PROCESS_ID_HELPER") == "1" {
		time.Sleep(30 * time.Second)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessIDReturnsExactStartedOwnedProcess$")
	command.Env = append(os.Environ(), "ENGORCH_PROCESS_ID_HELPER=1")
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	process := &Process{
		command: command, done: make(chan struct{}), cancel: cancel,
		launch: processLaunchIdentity{
			ExecutableSHA256: strings.Repeat("a", 64), AuthSHA256: strings.Repeat("b", 64),
			Root: t.TempDir(), WorkingDirectory: t.TempDir(), Endpoint: "http://127.0.0.1:1",
		},
	}
	go func() { process.err = command.Wait(); close(process.done) }()
	pid, err := process.ProcessID()
	if err != nil || pid != command.Process.Pid || pid <= 0 {
		t.Fatal("started owned process identifier mismatch", pid, command.Process.Pid, err)
	}
	closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_ = process.CloseContext(closeCtx)
	select {
	case <-process.Done():
	case <-closeCtx.Done():
		t.Fatal("owned helper process did not stop", closeCtx.Err())
	}
}
