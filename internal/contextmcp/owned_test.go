package contextmcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/repository"
)

func TestOwnedRunningBindsExactBrokerPointerAndBearer(t *testing.T) {
	broker := bridgeFixture(t)
	foreign := bridgeFixture(t)
	bearer := strings.Repeat("o", 40)
	server, err := NewOwned(broker, bearer, nil)
	if err != nil {
		t.Fatal(err)
	}
	running, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = running.Close(ctx)
		cancel()
		_ = running.Wait()
	}()
	if running.URL() == "" || running.CatalogHash() == "" || running.ValidateOwner(broker, bearer) != nil {
		t.Fatal("owned bridge lost constructor identities")
	}
	if err := running.ValidateOwner(foreign, bearer); err == nil {
		t.Fatal("same-catalog foreign broker pointer admitted")
	}
	if err := running.ValidateOwner(broker, strings.Repeat("x", 40)); err == nil {
		t.Fatal("foreign bearer admitted")
	}
	foreignServer, err := NewOwned(foreign, bearer, nil)
	if err != nil {
		t.Fatal(err)
	}
	foreignRunning, err := foreignServer.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = foreignRunning.Close(ctx)
		cancel()
		_ = foreignRunning.Wait()
	}()
	if err := foreignRunning.ValidateOwner(broker, bearer); err == nil {
		t.Fatal("same-catalog foreign owned listener admitted")
	}
}

func TestNewOwnedRejectsMissingBroker(t *testing.T) {
	if _, err := NewOwned(nil, strings.Repeat("o", 40), nil); err == nil {
		t.Fatal("nil broker admitted")
	}
}

func bridgeFixture(t *testing.T) *contextbroker.Broker {
	t.Helper()
	root := t.TempDir()
	git := func(arguments ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v: %s", err, output)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	source, err := repository.Discover(context.Background(), root, "owned-context-fixture")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextbroker.NewBinding(strings.Repeat("a", 64), source, nil, contextbroker.Limits{MaxCalls: 1, MaxRequestBytes: 4096, MaxResponseBytes: 64 << 10, MaxTotalResponseBytes: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := contextbroker.Open(filepath.Join(t.TempDir(), "broker.jsonl"), binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	return broker
}
