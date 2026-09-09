# TaskPool capacity and durability measurements

Executed on Windows amd64 / Ryzen 9 9955HX on 2026-09-08 with the local Go
1.27.1 toolchain. These measure the adapted capacity component, not completed
agent tasks, queue latency, model quality or total controller overhead.

The separate `TestSharedPoolAcrossProcesses` correctness test also passed on
September 8: six real child processes attempted distinct runs against one shared
pool with total capacity eight and provider capacity two. Exactly two attempts
remained active after all processes exited. This proves the tested cross-process
provider admission and unresolved-slot retention; it is not a throughput result.

| Active reservations | Median capacity decision | Allocations |
| --- | ---: | ---: |
| 1 | 38.93 ns | 0 |
| 32 | 584.4 ns | 0 |
| 1,024 | 16.119 us | 0 |

The capacity microbenchmark runs three samples, each with Go's one-second
calibration. It checks that the next request remains eligible. The implementation
scans active reservations, so this result exposes linear growth rather than
establishing constant-time scheduling. Raw output:
`.local/taskpool-capacity-benchmark-20260908.txt`.

A separate benchmark performs one durable acquire and settlement through public
APIs against a fresh bound SQLite pool. Three samples of 20 iterations yielded
a median **11.729 ms per pair**, about **100 KiB and 1,591 allocations**. Pool
creation and final accounting verification are outside timing; both journal
transactions and their validation are inside. Raw output:
`.local/taskpool-durable-final-20260908.txt`. A post-measurement source-hash
snapshot is retained in `.local/taskpool-benchmark-sources-20260908.json`; it is
not a build attestation. This is an empty-pool baseline and does
not characterize a long-lived pool's accumulated-history cost.

Correctness tests separately exercise 32 concurrent contenders for three slots,
all-applicable ceilings, denied-admission nonmutation, one-time release, retained
unresolved reservations and provider/model slash-identity separation. Ordinary
and race tests passed. The controller must still consume these leases before
dispatch and validate terminal evidence before settlement; that integration is
not yet proven by this component benchmark.

Host load and filesystem caches were uncontrolled. No tail percentile, memory
peak, provider price or end-to-end speedup follows from these measurements.

The exact-ID DAG adaptation was also executed on dependency chains. Three small
samples of three iterations measured validation plus downstream priority at
median 10.4 us (32 tasks), 196.8 us (1,024 tasks), and 12.30 ms (16,384 tasks).
The largest chain allocated about 35.4 MiB per construction; this is allocated
bytes, not peak RSS. Raw output is `.local/taskpool-dag-benchmark-20260908.txt`.
These are smoke measurements on chains only; dense/fan-in/fan-out workloads and
integrated ready-queue dispatch remain required. Failed dependencies do not
unblock descendants, and ties retain declaration order.

```powershell
go test ./internal/taskpool -run '^$' -bench BenchmarkCapacityDecision -benchtime=1s -count=3 -benchmem
go test ./internal/taskpool -run '^$' -bench BenchmarkDurableAcquireSettle -benchtime=20x -count=3 -benchmem
```
