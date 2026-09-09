# Persistent scheduler pump baseline

Local Windows/amd64 development measurement, 2026-09-08, Go 1.27.1,
AMD Ryzen 9 9955HX. This is an orchestration benchmark with a simulated runtime,
not a provider or model-quality comparison.

Each batch schedules 16 independent tasks against a fresh real scheduler
journal. Each adapter call waits 2ms. Setup is excluded; timed work includes
concurrent claims, replay, observations, 10ms polling and pump cancellation.
After timing, the benchmark verifies every task succeeded and dispatched once.
All nine batches passed. Three single-batch samples per worker count are too
few for statistical confidence or a production capacity claim.

| Workers | Median batch time | Median allocated bytes per batch |
| --- | ---: | ---: |
| 1 | 647.446ms | 73,707,248 |
| 2 | 444.932ms | 82,056,168 |
| 4 | 418.679ms | 127,004,064 |

The 2-to-4 worker increase yields little additional throughput in this short
workload while increasing allocations. The measured time includes journal
replay, competing claims and polling; it does not isolate any one of those
costs. Full journal replay and contention are candidates for profiling before
an optimization is attributed to either. These allocation totals are not peak
resident memory. CPU load, filesystem cache and antivirus were uncontrolled.

Reproduce from the repository root:

```text
go test ./internal/taskscheduler -run ^$ -bench ^BenchmarkPump$ -benchtime=1x -count=3 -timeout=90s
```

Source: `internal/taskscheduler/pump_benchmark_test.go`. The development run used
the repository-local Go toolchain; raw output is retained privately in
`.local/scheduler-pump-benchmark-20260908.txt`. No committed-build attestation,
external inference, recursive model conversation or pool-cost evidence is
claimed. The [mailbox baseline](agent-mailbox-performance.md) and
[runtime concurrency evaluation](agent-concurrency.md) measure different layers.
