package cli

import (
	"context"
	"strings"

	engtelemetry "harness.local/engorch/internal/telemetry"
)

func startCommandTelemetry(ctx context.Context, args []string) (context.Context, func(error)) {
	command := "unknown"
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--root" || argument == "-root" {
			index++
			continue
		}
		if strings.HasPrefix(argument, "--root=") || strings.HasPrefix(argument, "-root=") {
			continue
		}
		if !strings.HasPrefix(argument, "-") {
			command = argument
			break
		}
	}
	return engtelemetry.StartCommand(ctx, command)
}
