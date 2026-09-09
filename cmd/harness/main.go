// Command harness runs the local deterministic engineering harness.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"harness.local/engorch/internal/cli"
	engtelemetry "harness.local/engorch/internal/telemetry"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, telemetryRuntime, err := engtelemetry.New(ctx, engtelemetry.Config{OTLPTracesEndpoint: os.Getenv("ENGORCH_OTLP_TRACES_ENDPOINT")})
	if err == nil {
		cwd, cwdErr := os.Getwd()
		err = cwdErr
		if err == nil {
			err = cli.Execute(ctx, os.Args[1:], cwd, os.Stdout)
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = errors.Join(err, telemetryRuntime.Shutdown(shutdownCtx))
		cancel()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "harness:", err)
		os.Exit(1)
	}
}
