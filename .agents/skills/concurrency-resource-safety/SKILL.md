---
name: concurrency-resource-safety
description: Prevent context detachment, goroutine/handle leaks, unbounded reads, data races, and lock-order deadlocks in concurrent or I/O-heavy code. Use when changing HTTP handlers, subprocesses, channels, locks, timers, or async workflows.
metadata:
  fabric.roles: "writer,fixer,reviewer"
---

# Concurrency & Resource-Lifecycle Safety Protocol

Resource leaks and subtle races rarely fail on happy-path unit tests; enforce lifecycle invariants structurally.

## 1. Context Propagation & Timeouts
- Always propagate the caller's `ctx` downstream (`req.Context()`, `CommandContext`, `execWithContext`); never replace an active request/operation context with `context.Background()` unless intentionally detaching async work with an explicit timeout and documented reason.
- Pair every `context.WithTimeout` / `context.WithCancel` / `signal.NotifyContext` with an immediate `defer cancel()` / `defer stop()`.
- Set explicit network, server (`ReadHeaderTimeout`), and HTTP client timeouts on every external call.

## 2. Bounded Resources & Handle Cleanup
- Close every file, HTTP response body (`defer resp.Body.Close()`), DB cursor, and stream in the owning scope.
- Bound untrusted reads (`io.LimitReader` / `http.MaxBytesReader`), channel buffers, and worker pools so malformed inputs cannot exhaust memory.
- Clean up spawned child processes/process groups and temporary worktrees on both error and timeout paths.

## 3. Race & Deadlock Discipline
- Acquire multiple locks in one documented global order, keep lock hold times O(1) (never hold a mutex across network/disk/subprocess I/O), and never leak goroutines blocked on unbuffered channels.
- Guard shared mutable maps/caches with `sync.Mutex` / `sync.RWMutex` or `sync.Map`.
- Verify concurrent or stateful changes under race detection (`go test -race ...`) when supported on the host toolchain.
