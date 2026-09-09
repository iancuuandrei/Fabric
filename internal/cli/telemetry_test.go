package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	engtelemetry "harness.local/engorch/internal/telemetry"
)

func TestExecuteExportsCommandSpanWithoutArguments(t *testing.T) {
	var export []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var err error
		export, err = io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		writer.Header().Set("Content-Type", "application/x-protobuf")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, runtime, err := engtelemetry.New(context.Background(), engtelemetry.Config{OTLPTracesEndpoint: server.URL + "/v1/traces"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := Execute(ctx, []string{"--root", root, "help"}, root, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	encoded := string(export)
	for _, want := range []string{"engorch.command", "engorch.outcome", "help", "ok"} {
		if !strings.Contains(encoded, want) {
			t.Errorf("exported span does not contain %q", want)
		}
	}
	if strings.Contains(encoded, root) || strings.Contains(encoded, "--root") {
		t.Fatal("exported span contains CLI arguments")
	}
}
