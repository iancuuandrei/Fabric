# CLI startup measurement

`scripts/benchmark_startup.py` measures a compiled `harness help` process with
standard-library Python tooling. Build the binary first, then pass its path and
`--go` when the Go executable is outside PATH. The default is three warmup runs
followed by thirty samples. A --conditions note is required. Each process has a thirty-second timeout; failed,
empty, oversized or inconsistent help responses abort measurement.

The JSON report retains every elapsed nanosecond sample, median and nearest-rank
p95, warmup count, OS/architecture/processor/logical CPUs, physical RAM and CPU model on Windows, Python version, embedded
Go build metadata, binary size/hash and help-output hash. It hashes the tracked
and nonignored untracked source inventory before and after sampling and rejects
changes. HEAD is null for an unborn repository; that is not an exact-commit release
receipt. The inventory describes current development files, not proof that those
files produced the supplied binary.

Timing includes native process launch and output capture. Warm OS caches are not
flushed; host idleness is not guaranteed. On Windows, each child reports peak working-set bytes through GetProcessMemoryInfo; other platforms retain null. This
is help-command startup, not controller overhead, model latency, RI performance or
context savings. Do not turn these results into product claims without the broader
required measurements and fixed-source qualification.

Executed smoke measurement on the current Windows amd64 development host using
Go1.27.1: thirty successful samples after three warmups, stable output and source
inventory. Private report: `.local/startup-benchmark.json`. The general Go suite
was concurrently active, so this is functionality evidence for the measurement
script, not an unloaded performance baseline. Fresh controlled measurements and
remaining performance dimensions are pending.


The memory-enabled revision executed another thirty samples after the Go suite
terminated. Its `.local/startup-benchmark-memory.json` records the explicit note
that other host workloads remained uncontrolled, physical RAM, CPU model and
per-sample memory values. The Windows metric is
[PeakWorkingSetSize](https://learn.microsoft.com/en-us/windows/win32/api/psapi/ns-psapi-process_memory_counters),
not allocation count, heap size or private bytes. Hardware collection and memory
query overhead are outside the measured launch/communicate interval. This run
is not a before/after performance comparison with the earlier smoke measurement.
