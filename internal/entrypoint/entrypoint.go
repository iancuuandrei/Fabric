package entrypoint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/iancuuandrei/fabric/internal/cli"
	engtelemetry "github.com/iancuuandrei/fabric/internal/telemetry"
)

// executeFunc runs one CLI invocation without exiting the process.
type executeFunc func(ctx context.Context, args []string, cwd string, out io.Writer) error

// Run bootstraps one native CLI process: interrupt-aware context, telemetry
// runtime with bounded shutdown, working-directory discovery, CLI execution
// and prefixed stderr reporting with exit code 1 on failure. args are the
// process arguments without the program name; cwd is the resolved working
// directory. execute defaults to cli.Execute when nil so tests can inject a
// stub without a framework.
func Run(name string, args []string, execute executeFunc) {
	if execute == nil {
		execute = cli.Execute
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	otlpEndpoint := os.Getenv("FABRIC_OTLP_TRACES_ENDPOINT")
	if otlpEndpoint == "" {
		otlpEndpoint = os.Getenv("ENGORCH_OTLP_TRACES_ENDPOINT")
	}
	ctx, telemetryRuntime, err := engtelemetry.New(ctx, engtelemetry.Config{OTLPTracesEndpoint: otlpEndpoint})
	if err == nil {
		cwd, cwdErr := os.Getwd()
		err = cwdErr
		if err == nil {
			err = execute(ctx, args, cwd, os.Stdout)
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = errors.Join(err, telemetryRuntime.Shutdown(shutdownCtx))
		cancel()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, name+":", err)
		os.Exit(1)
	}
}
