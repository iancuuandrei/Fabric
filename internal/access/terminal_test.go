package access

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func terminalFixture(t *testing.T) (string, Policy, Intent, Receipt) {
	t.Helper()
	path, policy, intent := activeFixture(t)
	routeID, err := intent.Route.ID()
	if err != nil {
		t.Fatal(err)
	}
	receipt := Receipt{
		InvocationID: intent.Reservation.InvocationID,
		RouteID:      routeID,
		Status:       "completed",
		OutputHash:   strings.Repeat("c", 64),
	}
	return path, policy, intent, receipt
}

func TestRequireTerminalAdmitsExactHistory(t *testing.T) {
	path, policy, intent, receipt := terminalFixture(t)
	if err := ReserveDurable(path, policy, intent); err != nil {
		t.Fatal(err)
	}
	if err := RecordTerminal(path, policy, receipt); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := RequireTerminal(path, policy, intent, receipt); err != nil {
		t.Fatal("exact terminal history rejected:", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("terminal inspection mutated journal", err)
	}
}

func TestRequireTerminalRejectsForeignIntentAndReceipt(t *testing.T) {
	path, policy, intent, receipt := terminalFixture(t)
	if err := ReserveDurable(path, policy, intent); err != nil {
		t.Fatal(err)
	}
	if err := RecordTerminal(path, policy, receipt); err != nil {
		t.Fatal(err)
	}

	foreignIntent := intent
	foreignIntent.Attempt++
	foreignIntent.Reservation.InvocationID, _ = foreignIntent.ID()
	foreignReceipt := receipt
	foreignReceipt.InvocationID = foreignIntent.Reservation.InvocationID
	if err := RequireTerminal(path, policy, foreignIntent, foreignReceipt); err == nil {
		t.Fatal("foreign terminal intent accepted")
	}

	changedReceipt := receipt
	changedReceipt.Status = "failed"
	changedReceipt.OutputHash = ""
	if err := RequireTerminal(path, policy, intent, changedReceipt); err == nil {
		t.Fatal("foreign terminal receipt accepted")
	}
}

func TestRequireTerminalRejectsActiveAndMissing(t *testing.T) {
	path, policy, intent, receipt := terminalFixture(t)
	if err := RequireTerminal(path, policy, intent, receipt); err == nil {
		t.Fatal("missing terminal history accepted")
	}
	if err := ReserveDurable(path, policy, intent); err != nil {
		t.Fatal(err)
	}
	if err := RequireTerminal(path, policy, intent, receipt); err == nil {
		t.Fatal("active reservation accepted as terminal")
	}
}

func TestRequireTerminalRejectsMalformedHistoryAndPolicyDrift(t *testing.T) {
	path, policy, intent, receipt := terminalFixture(t)
	if err := ReserveDurable(path, policy, intent); err != nil {
		t.Fatal(err)
	}
	if err := RecordTerminal(path, policy, receipt); err != nil {
		t.Fatal(err)
	}
	drifted := policy
	drifted.RunID = strings.Repeat("d", 64)
	if err := RequireTerminal(path, drifted, intent, receipt); err == nil {
		t.Fatal("terminal history replayed under drifted policy")
	}

	malformed := filepath.Join(t.TempDir(), "access.jsonl")
	if err := os.WriteFile(malformed, []byte("{not-json}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RequireTerminal(malformed, policy, intent, receipt); err == nil {
		t.Fatal("malformed terminal history accepted")
	}
}
