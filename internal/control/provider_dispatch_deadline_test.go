package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"harness.local/engorch/internal/config"
)

func TestOpenCodeProviderRuntimeContextAddsFiniteTotalDeadline(t *testing.T) {
	ctx, cancel := openCodeProviderRuntimeContext(context.Background(), nil)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("OpenCode provider runtime context has no total deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > openCodeProviderRuntimeTimeout {
		t.Fatalf("OpenCode provider runtime deadline is outside its finite bound: %s", remaining)
	}
}

func TestOpenCodeProviderRuntimeContextPreservesEarlierDeadlineAndCancellation(t *testing.T) {
	callerDeadline := time.Now().Add(time.Minute)
	parent, parentCancel := context.WithDeadline(context.Background(), callerDeadline)
	defer parentCancel()
	ctx, cancel := openCodeProviderRuntimeContext(parent, nil)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.Equal(callerDeadline) {
		t.Fatalf("OpenCode provider runtime extended caller deadline: got=%s want=%s", deadline, callerDeadline)
	}

	canceledParent, cancelParent := context.WithCancel(context.Background())
	canceled, cancelRuntime := openCodeProviderRuntimeContext(canceledParent, nil)
	cancelParent()
	defer cancelRuntime()
	select {
	case <-canceled.Done():
		if !errors.Is(canceled.Err(), context.Canceled) {
			t.Fatal("OpenCode provider runtime changed caller cancellation", canceled.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("OpenCode provider runtime did not preserve caller cancellation")
	}
}

func TestOpenCodeConfiguredDeadlinesRemainIndependent(t *testing.T) {
	host := &config.OpenCodeHost{InvocationTimeoutSeconds: 7200, ReadbackTimeoutSeconds: 10}
	ctx, cancel := openCodeProviderRuntimeContext(context.Background(), host)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < 119*time.Minute || time.Until(deadline) > 2*time.Hour {
		t.Fatal("configured invocation deadline not honored", deadline)
	}
	if openCodeReadbackTimeout(host) != 10*time.Second {
		t.Fatal("readback deadline mixed with invocation")
	}
	earlier, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	child, finish := openCodeProviderRuntimeContext(earlier, host)
	defer finish()
	want, _ := earlier.Deadline()
	got, _ := child.Deadline()
	if !got.Equal(want) {
		t.Fatal("configured deadline extended caller authority")
	}
}
