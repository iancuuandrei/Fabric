package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/opencode"
)

func TestAutonomousFailureIncludesSanitizedRuntimeStage(t *testing.T) {
	path, s, out := planAutonomousFailureFixture(t, "runtime diagnostic fixture")
	client, err := opencode.NewClient("http://127.0.0.1:1", "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, cause := client.SubmitSynchronousToolTurn(ctx, "", "", opencode.SynchronousToolDispatchIntent{})
	cause = errors.Join(cause, errors.New("TOKEN=private"))
	boundary := reportAutonomousFailure(&out, path, s.RunID, cause)
	var summary autonomousFailure
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if boundary == nil || summary.RuntimeDiagnostic == nil || summary.RuntimeDiagnostic.Stage != "preflight" || strings.Contains(out.String(), "private") || strings.Contains(boundary.Error(), "private") {
		t.Fatal("stage was lost or private data leaked", boundary, out.String())
	}
	out.Reset()
	_ = reportAutonomousFailure(&out, path, s.RunID, errors.New("response_decode TOKEN=private"))
	if strings.Contains(out.String(), "runtime_diagnostic") || strings.Contains(out.String(), "private") {
		t.Fatal("arbitrary text became a diagnostic", out.String())
	}
}
