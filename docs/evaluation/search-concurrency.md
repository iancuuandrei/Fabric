# Search concurrency benchmark

Executed locally on Windows on 2026-09-08, using the newly rebuilt release RI
binary and a retained deterministic 10,000-file corpus. Exact-result validation
passed for every query, including full-scan fallback reporting. Raw measurements,
binary/runner/index identities and limitations are in
`.local/search-concurrency-10k-20260908.json`.

| Concurrent RI processes | Selective fixed query, median queries/s | Case-insensitive full scan, median queries/s |
| --- | ---: | ---: |
| 1 | 32.07 | 0.317 |
| 2 | 48.07 | 0.757 |
| 4 | 70.46 | 0.887 |

Each process serves five requests through the persistent framed transport. There
is one discarded warmup per query mode and three measured rounds per concurrency
level. Throughput divides completed verified queries by batch wall time, including
process startup and executor dispatch. All raw per-process batch times are retained.
No percentile or per-query latency claim is made from these small samples.

The case-insensitive request deliberately exercises the conservative full-scan
path. It has the same expected hits; the comparison measures two execution paths,
not two engines implementing an identical optimization strategy. The gap warrants
profiling scan/validation/I/O costs before changing correctness-sensitive behavior.

The host had concurrent development workloads and uncontrolled OS caches. This is
a synthetic selective-query benchmark, not representative semantic-search quality,
agent task success, model latency or cost. It does not establish a speedup against
ripgrep. Follow-up workloads should include real repository distributions, regex
fallbacks, overlays, symbol/reference queries and fixed-task agent comparisons.

Reproduce with a fresh output filename:

```powershell
cargo build --release -p engorch-ri --bin engorch-ri
python scripts/benchmark_search_concurrency.py target/release/engorch-ri.exe .local/lexical-benchmark-release-10k-01 .local/search-concurrency-new.json --rounds 3
```

The corpus can be created with `scripts/benchmark_lexical.py`; that runner also
performs an executed ripgrep comparison. Agent/orchestration benchmarks are a
separate pending qualification: actual pinned runtime with controlled provider
fixtures first, then explicitly configured real-model task quality and token cost.
