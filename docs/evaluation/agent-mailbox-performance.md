# Durable agent mailbox baseline

Executed on Windows amd64, AMD Ryzen 9 9955HX, Go 1.27.1 on September 8,
2026. These measurements use real AgentTree and AgentControl journals with
1,024-byte message bodies. They measure control-plane persistence and replay;
no model, runtime consumption, delivery acknowledgement, or agent task quality
is measured.

| Operation | History | Median time | Observed time range | Median allocated bytes |
| --- | ---: | ---: | ---: | ---: |
| Read last page (up to 16 messages) | 1 | 4.830 ms | 4.259–6.328 ms | 99,016 |
| Read last page (up to 16 messages) | 32 | 9.944 ms | 9.657–10.687 ms | 2,403,061 |
| Read last page (up to 16 messages) | 128 | 30.733 ms | 22.808–70.994 ms | 9,488,189 |
| Send one message | 0 | 48.294 ms | 46.856–66.053 ms | 401,384 |

Each row has three samples of three operations. Journal creation and history
seeding are outside the measured interval. Reads include complete journal
validation and verify the returned count and exact bodies. Sends use fresh
bound journals for each operation and verify the returned body and non-waking
envelope. Allocated bytes are cumulative allocations per operation, not RSS.

The host and filesystem cache were uncontrolled, with other development work
in progress. The small sample and wide observed range do not establish a
latency percentile, performance regression threshold, or throughput SLA.

The current read path reconstructs the entire message journal before returning
a bounded page. The growing allocation cost identifies an implementation
limit: page bounds do not bound journal replay work. A future incremental
projection must retain exact journal authority and tamper detection; these
measurements do not justify bypassing validation.

Reproduce from the repository root:

```powershell
.local/toolchains/go/bin/go.exe test ./internal/agentcontrol -run '^$' -bench BenchmarkMailbox -benchtime=3x -count=3 -timeout=120s
```

The executed benchmark source is `internal/agentcontrol/benchmark_test.go`.
Raw local output is `.local/agent-mailbox-benchmark-20260908.txt`; this local
baseline has no final commit or compiled-artifact attestation. The separate
[runtime concurrency evaluation](agent-concurrency.md) measures actual OpenCode
processes, while [TaskPool measurements](taskpool-performance.md) cover capacity
decisions and durable reservations.

## Activity pages with exact journal heads

The atomic activity-page API was measured separately on the same Windows host
and Go version. Each of three samples used Go's adaptive 500 ms benchmark
interval. Setup was excluded; every measured read checked the exact journal
head, activity identities and count against the fixed baseline.

| History | Median time | Observed time range | Median allocated bytes |
| ---: | ---: | ---: | ---: |
| 1 | 5.532 ms | 5.297–5.904 ms | 100,144 |
| 32 | 10.645 ms | 10.501–11.010 ms | 2,417,032 |
| 128 | 22.257 ms | 21.612–23.287 ms | 9,548,885 |

These are activity metadata pages of at most 16 items, with journal validation
and replay included. They do not measure accepted-result body resolution,
model latency or end-to-end parent consumption. The earlier mailbox figures
used different sampling, and neither run controlled host load or cache state;
the tables do not establish a performance improvement or a latency percentile.

```powershell
.local/toolchains/go/bin/go.exe test -p 1 ./internal/agentcontrol -run '^$' -bench '^BenchmarkActivityPageWithHead$' -benchtime=500ms -count=3 -benchmem
```

The local output `.local/benchmark-activity-page-with-head.txt` has SHA-256
`ed6a1b20faf274a3dfc6ef887dd667117a0d338e438f5bfc24f549889d072670`.
Post-run source fingerprints are
`d7cb84e506048122255e440454c6b539f7fb535edd4cf88d313a95dff41b53ba`
for `internal/agentcontrol/benchmark_test.go` and
`ce69b5c880668b1c42c9dd7310bec89fc5818538fd68154152677b662e94525e`
for `internal/agentcontrol/page.go`. These identify the local files; they are
not a complete build-input or final-commit attestation.
