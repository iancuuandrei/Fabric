# Lexical measurements

`scripts/benchmark_lexical.py BINARY FRESH_DIRECTORY --files N` runs the actual
binary and installed `rg` against a deterministic corpus at 10,000, 100,000 or
500,000 files. It writes exact manifest records, native Git blob identities,
raw sources, a staged index and result.json. Repository/commit/tree identities
are explicitly synthetic; this is not Git membership or controller qualification.
The directory must not already exist; failed runs retain their partial evidence.

The current corpus contains about 1 KiB per file and one fixed literal every
1,000 files. Both engines must return exactly the expected path/byte intervals.
Five fresh-process queries and a five-request persistent batch are measured,
plus five rg queries. Persistent timing includes one process startup. Each child
has a ten-minute timeout. Caches are not flushed. The updated runner captures
Windows lifetime peak working set separately for each child (null elsewhere),
records runner/source inventory hashes and rejects source/binary drift. Older
debug measurements below predate this memory instrumentation.
This is an initial selective-query evaluation, not the full requested benchmark:
regex/fallback workloads, overlays, cold-cache conditions, release builds, memory
and model-level tool/token comparisons remain to be qualified.

Executed locally on Windows 11 with a debug RI binary, 10,000 files:

| Measurement | Observation |
| --- | ---: |
| Index build | 6.343 s |
| Fresh-process RI query median (five samples) | 0.644 s |
| Persistent five-request batch, including startup | 2.553 s |
| rg query median (five samples) | 1.297 s |
| Exact result agreement | PASS, ten matches per query |

Binary SHA-256: `7c54163319fb54f92bb9a186bbd8d12d9ab901e25eb8fc5dd5fe43600f535430`.
Manifest ID: `4ba54b9951b7dbe54f11c6c610fd9cc4afcb978c76647eddeb50ea9b862ae7ea`.
Raw local evidence is retained in `.local/lexical-benchmark-10k-01/result.json`.
These observations do not establish tail latency, representative repository
performance or release acceptance.

The same binary and query were subsequently executed on 100,000 files:

| Measurement | Observation |
| --- | ---: |
| Index build | 88.707 s |
| Fresh-process RI query median (five samples) | 9.198 s |
| Persistent five-request batch, including startup | 45.403 s |
| rg query median (five samples) | 14.899 s |
| Exact result agreement | PASS, 100 matches per query |

Manifest ID: `356cf8fb024e38e33813bfdba465197c2f8f4a040026760d9438929d78cbd8bd`.
Raw local evidence: `.local/lexical-benchmark-100k-01/result.json`.
The persistent batch remains close to five fresh-process queries; these data
do not demonstrate a substantial retention benefit. Repeated artifact validation
and candidate construction require profiling before attributing that cost.
The broader query and overlay workloads remain unexecuted.

An optimized Cargo release build was subsequently qualified on the same 10k
corpus using the updated runner. Exact agreement passed for every query:

| Measurement | Observation |
| --- | ---: |
| Index build | 1.698 s |
| Fresh-process RI median | 103.305 ms |
| Persistent five-request batch | 262.050 ms |
| rg median | 1.224 s |
| Build peak working set | 11,051,008 bytes |
| Largest one-shot peak working set | 10,010,624 bytes |
| Persistent peak working set | 14,438,400 bytes |

Release binary SHA-256:
`f35781bc577cc367de1365b02cadd13f3d6c8e60bf80c2fc8602f4a1c476dd84`.
Local receipt: `.local/lexical-benchmark-release-10k-01/result.json`.
The build command completed as Cargo's optimized release profile. The receipt's
profile field is nevertheless caller-declared, not embedded build attestation.
Peak working set is not a portable allocator limit or total system-memory metric.

The same release binary was then run at both larger corpus sizes. Every query
passed exact path/byte-range agreement, with 100 and 500 matches respectively.

| Measurement | 100k files | 500k files |
| --- | ---: | ---: |
| Index build | 32.876 s | 166.027 s |
| Fresh-process RI median | 1.284 s | 3.649 s |
| Persistent five-request batch | 4.311 s | 14.957 s |
| rg median | 18.475 s | 60.396 s |
| Build peak working set | 53,014,528 bytes | 245,739,520 bytes |
| Largest one-shot peak working set | 53,542,912 bytes | 247,148,544 bytes |
| Persistent peak working set | 91,000,832 bytes | 429,424,640 bytes |

Local receipts: `.local/lexical-benchmark-release-100k-01/result.json` and
`.local/lexical-benchmark-release-500k-01/result.json`.
500k manifest: `3bd3afee41fa4c767bbd24b13a199798bcf5dff6e7eca5c3239ec375df542143`.
Both runs bind development inventory
`9146604d19d9e31686f7b7d59684c9ca09ad10f6445b3ab76098d93d90395ad6`
and runner `967440a2dd5a76b7e434fc113ea633d9f4f19b3d409a13f3024bea5b80123d2e`.

This qualifies the direct Rust protocol for this generated corpus at its 500k
file ceiling. It does not qualify the Go controller's full lifecycle at that size,
whose candidate/inline-reference limits remain separate. Persistent memory grows
with manifest and lookup metadata; these observations are not constant-memory
proof. Profiling and reducing the retained/revalidated metadata footprint remains
work, alongside overlay/fallback/model evaluations and actual repository workloads.

After removing full manifest copies, `benchmark_lexical_replay.py` re-ran five
persistent queries on the retained 500k corpus and index. Every query again
matched all 500 exact expected locations. Peak working set was **211,374,080
bytes**, versus 429,424,640 previously; total batch time was **8.525 s**, versus
14.957 s previously. This is one subsequent measurement with uncontrolled cache
and host load, not an isolated causal latency estimate or a new rg comparison.

New release SHA-256:
`b52bb90bad566d13f0b84c0481f39c1b3eb1f96aa2740c56a9ea3b9914f97731`.
Local receipt: `.local/lexical-memory-replay-500k-01.json`.
The old runner did not retain its build receipt, so this replay reconstructs and
records the current shard hashes. It does not claim exact original index-byte
identity based solely on the old receipt. Future complete runs now save
`build-receipt.json` immediately after verified build success.

The replay runner also supports `--overlay`: four existing matching files are
replaced with nonmatching bytes, two new matching files are added, and two
existing matching files are deleted. Only changed artifacts are written in a
fresh overlay directory; no base-index build is invoked. The synthetic candidate
fingerprint is explicitly fixture identity, not controller authority.

On the retained 500k base, five overlay queries returned exactly **496 matches**
each, with the expected overlay identity. Total batch time was **7.157 s** and
peak working set **211,697,664 bytes**. Receipt:
`.local/lexical-overlay-500k-01.json`; release binary is the `b52bb90b...` build
above. This does not measure governed candidate capture or incremental-controller
latency, nor does a faster subsequent batch establish an overlay speedup.

`--fallback` uses case-insensitive matching to require the conservative full-scan
path and verifies `full_scan: true` in every response. Combined with `--overlay`
on the retained 10k corpus, five queries returned exactly **six matches** each.
Batch time: **17.146 s**; peak working set: **10,563,584 bytes**. Receipt:
`.local/lexical-fallback-overlay-10k-01.json`. Full-scan larger-corpus and varied
regex/Unicode workloads, plus model-level comparisons, remain outstanding.
