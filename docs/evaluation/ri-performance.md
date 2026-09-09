# Repository intelligence measurement

Build `cargo build --release --example benchmark_snapshot`, then run
`scripts/benchmark_snapshot.py target/release/examples/benchmark_snapshot.exe
--conditions "explicit workload conditions"` on Windows. Use the platform binary
name elsewhere. The wrapper requires Python and rustc on PATH; it records host
rustc metadata, not an embedded compiler attestation.

The Rust example constructs deterministic synthetic directed rings with 1000 and
10000 symbol nodes and the same number of observed reference edges. Every query
has out-degree one, so unrelated graph growth is separated from output growth.
Each dataset has one discarded warmup and five measured samples. Each sample
separately times snapshot construction, full verified in-memory load, and a batch
of 1000 queries through the validated Snapshot API. Input cloning is outside build
timing; query identity, validation and result allocation are inside query timing.
The fixture checks identical snapshot IDs and exact one-edge query results.

The wrapper retains all samples and medians, snapshot identity/size, binary hash
and size, source inventory/hash, host/compiler metadata, UTC time and explicit
workload conditions. Source or binary changes during measurement abort the report.
Source IDs in the synthetic graph are fixture labels, never real repository proof.
An unborn HEAD is null; the development inventory does not attest the build.

Executed on the current Windows amd64 host with the release example and recorded
Rust toolchain: both datasets completed, snapshot identities stayed stable and
Clippy passed with warnings denied. Local evidence is
`.local/ri-snapshot-benchmark-evidence.json`. The report records other host workloads
as uncontrolled. This qualifies the measurement path, not a product SLA or an
optimization claim.

Remaining measurements include real repository distributions, occurrence-heavy
queries, changed/impact queries, disk build/reuse, compiler producer time, per-phase
memory and allocations. This example does not measure these; per-dataset memory remains null.
Five samples cannot establish useful tail latency. CLI startup and model latency
are separate measurements and must not be added to these numbers without an
explicit end-to-end experiment.

The wrapper now also records whole-process elapsed time and, on Windows, lifetime
peak working-set bytes through the same sampler as CLI startup. This includes
both graph sizes, input construction/cloning, warmups and every measured phase.
It cannot be attributed to one graph size or query; per-dataset peak-memory fields
remain null. Other platforms retain null until a corresponding metric is implemented.
The whole-process timer also includes launch/output capture and is separate from
Rust's internal phase timers.

Executed `.local/ri-snapshot-benchmark-memory.json` contains nonnull Windows
whole-process memory and both datasets with the same validity checks. A separate
five-sample help run verified the shared sampler still serves startup measurement.
These runs retain explicit uncontrolled-host-workload notes. No memory optimization
or comparative speed claim follows from the new measurement alone.

## Supplied SCIP snapshot

Build `cargo build --release --example benchmark_existing` and invoke the wrapper
with that binary and `--request REQUEST_JSON`. The request must be canonical JSON
with exactly `snapshot_path`, `snapshot_id`, `source` and `query`. Source is the
snapshot's repository identity; query uses the occurrence-query schema. Each
sample verifies the complete snapshot against the supplied identity before timing
1000 occurrence queries. A nonempty result and stable canonical result hash are
required. Snapshot bytes and request bytes must remain unchanged during the run.
File reading and compiler production are outside the internal load timer.

The retained scip-go integration fixture completed one warmup and five samples.
Its actual Git HEAD and tree matched the snapshot source. The 2891-byte snapshot
returned one definition; median verified load was 41900 ns and median batch of
1000 queries was 704700 ns. Whole-process peak working set was 5296128 bytes.
Evidence is `.local/ri-existing-benchmark-evidence.json`, including exact source,
snapshot, binary and request hashes. This tiny fixture validates the measurement
path, not performance on a representative repository. Other host load was
uncontrolled and no tail-latency claim is supported.

Negative executions rejected a different snapshot hash, different source commit,
and an unknown query symbol without emitting a benchmark report. Their retained
results are `.local/ri-existing-negative-evidence.json`. Clippy passed for all
workspace targets with warnings denied.
