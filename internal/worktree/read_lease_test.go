package worktree

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadLeaseSharesAndExcludesWriter(t *testing.T) {
	_, request := fixture(t)
	first, err := AcquireRead(request)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := AcquireRead(request)
	if err != nil {
		t.Fatal("second reader was serialized", err)
	}
	if _, err := Acquire(request); !errors.Is(err, ErrLeaseContention) {
		t.Fatal("writer contention not identified", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(request); err == nil {
		t.Fatal("writer acquired guard while one reader remained")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err := Acquire(request)
	if err != nil {
		t.Fatal("writer did not acquire after readers closed", err)
	}
	if _, err := AcquireRead(request); !errors.Is(err, ErrLeaseContention) {
		t.Fatal("reader contention not identified", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadLeaseOwnershipIsExactAndCloseIsIdempotent(t *testing.T) {
	_, request := fixture(t)
	lease, err := AcquireRead(request)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("visit result")
	if err := lease.WithOwnership(request, func(identity LeaseIdentity) error {
		if identity.Request != request {
			t.Fatal("read identity changed request")
		}
		return want
	}); !errors.Is(err, want) {
		t.Fatal("visit result was not retained", err)
	}
	changed := request
	changed.Source.Commit = strings.Repeat("b", len(request.Source.Commit))
	if err := lease.WithOwnership(changed, func(LeaseIdentity) error { return nil }); err == nil {
		t.Fatal("read lease accepted substituted request")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal("second close was not idempotent", err)
	}
	if err := lease.WithOwnership(request, func(LeaseIdentity) error { return nil }); err == nil {
		t.Fatal("closed read lease retained ownership")
	}
}

func TestReadLeaseWithOwnershipSerializesClose(t *testing.T) {
	_, request := fixture(t)
	lease, err := AcquireRead(request)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	visitDone := make(chan error, 1)
	go func() {
		visitDone <- lease.WithOwnership(request, func(LeaseIdentity) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- lease.Close() }()
	select {
	case err := <-closeDone:
		t.Fatal("Close returned while read ownership callback was active", err)
	default:
	}
	close(release)
	if err := <-visitDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}

func TestReadAndWriterLeasesRejectGuardMutation(t *testing.T) {
	_, request := fixture(t)
	reader, err := AcquireRead(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reader.path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := reader.WithOwnership(request, func(LeaseIdentity) error { return nil }); err == nil {
		t.Fatal("read lease accepted changed guard")
	}
	if err := reader.Close(); err == nil {
		t.Fatal("read lease closed changed guard without ownership error")
	}
	if err := os.WriteFile(reader.path, nil, 0600); err != nil {
		t.Fatal(err)
	}

	writer, err := Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(writer.guardPath, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writer.WithOwnership(request, func(LeaseIdentity) error { return nil }); err == nil {
		t.Fatal("writer lease accepted changed exclusion guard")
	}
	if err := writer.Close(); err == nil {
		t.Fatal("writer lease closed changed guard without ownership error")
	}
	if _, err := os.Lstat(writer.path); err != nil {
		t.Fatal("writer token was removed after guard ownership failure", err)
	}
	if err := os.Remove(writer.path); err != nil {
		t.Fatal(err)
	}
}

func TestReadLeaseCrossProcessSharingWriterExclusionAndCrashRelease(t *testing.T) {
	_, request := fixture(t)
	reader, err := AcquireRead(request)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := runLeaseGuardHelper(request, "read-once"); err != nil {
		t.Fatal("cross-process reader was serialized", err, string(output))
	}
	if output, err := runLeaseGuardHelper(request, "write-once"); err == nil {
		t.Fatal("cross-process writer acquired active reader guard", string(output))
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if output, err := runLeaseGuardHelper(request, "read-crash"); err != nil {
		t.Fatal("crashed reader helper failed", err, string(output))
	}
	writer, err := Acquire(request)
	if err != nil {
		t.Fatal("reader process exit retained kernel guard", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCrashedWriterTokenBlocksReadersAfterKernelRelease(t *testing.T) {
	_, request := fixture(t)
	if output, err := runLeaseGuardHelper(request, "write-crash"); err != nil {
		t.Fatal("crashed writer helper failed", err, string(output))
	}
	if _, err := AcquireRead(request); err == nil || errors.Is(err, ErrLeaseContention) || !strings.Contains(err.Error(), "writer lease token") {
		t.Fatal("stale durable writer token did not block reader", err)
	}
	if _, err := Acquire(request); err == nil {
		t.Fatal("stale durable writer token did not block writer")
	}
	lockPath := filepath.Join(request.Source.Root, ".harness", "leases", request.RunID+".lock")
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
}

func runLeaseGuardHelper(request Request, mode string) ([]byte, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLeaseGuardProcessHelper$")
	cmd.Env = append(os.Environ(), "ENGORCH_LEASE_GUARD_HELPER="+mode, "ENGORCH_LEASE_GUARD_REQUEST="+base64.RawStdEncoding.EncodeToString(raw))
	return cmd.CombinedOutput()
}

func TestLeaseGuardProcessHelper(t *testing.T) {
	mode := os.Getenv("ENGORCH_LEASE_GUARD_HELPER")
	if mode == "" {
		return
	}
	raw, err := base64.RawStdEncoding.DecodeString(os.Getenv("ENGORCH_LEASE_GUARD_REQUEST"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var request Request
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&request); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	switch mode {
	case "read-once", "read-crash":
		lease, err := AcquireRead(request)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		if mode == "read-once" {
			if err := lease.Close(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(4)
			}
		}
		os.Exit(0)
	case "write-once", "write-crash":
		lease, err := Acquire(request)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		if mode == "write-once" {
			if err := lease.Close(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(4)
			}
		}
		os.Exit(0)
	default:
		fmt.Fprintln(os.Stderr, "unknown helper mode")
		os.Exit(2)
	}
}
