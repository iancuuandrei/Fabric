# Actual OpenCode concurrency qualification

## Source-inventory checkpoint, September 8

The updated compiled-once matrix passed **12/12 executions**, with zero errors
and the fresh pre-intent cancellation check passing. Each level completed four
independent production `Execute` source-tool workflows against a local TLS
provider fixture. The mixed `source_read`/`list_agents` controller test is a
separate qualification; this matrix does not measure its queue throughput.

| Concurrent executions | Completed | Four-task batch, ms | Per-task samples, ms |
| --- | ---: | ---: | --- |
| 1 | 4/4 | 29181.460 | 7353.473, 7213.739, 7272.074, 7342.172 |
| 2 | 4/4 | 16311.915 | 7844.064, 8140.058, 8343.579, 8171.856 |
| 4 | 4/4 | 10095.644 | 10079.134, 9404.057, 10095.644, 10077.388 |

Four concurrent executions completed the batch about 2.89 times as fast as one,
while individual task latency increased. These are small, uncontrolled-host
samples, not tail-latency, supported-capacity or external model-quality claims.
Every execution checked two provider requests, one broker call, terminal sealing
and offline recovery without another provider request.

The raw report is `.local/opencode-execute-benchmark-queued-composite.json`,
SHA-256 `62f7f9dfb7d4f9ec2be7fe7659d90a58775e4e3084ed98d3cd004adec28df236`.
It binds test binary
`0253b1c11e76c285e2f3ab14e8060cd226abdd02d5b2e854559477bfd02d6dc5`
and the same pinned OpenCode executable identified below. The 802-file
development inventory was unchanged before compilation, after compilation and
after the matrix: `d032e7f0fe4b08492f70fe8f129bc6dc5d6da5e30b7294071d36b48f7943d712`.
HEAD was null because the standalone repository remains uncommitted. This
inventory covers tracked and nonignored untracked files; it excludes ignored
inputs and external dependencies, cannot exclude change-and-revert between
snapshots, and does not claim a hermetic build. This report documentation was
updated after that measured snapshot.

## Earlier health-first checkpoint

The earlier compiled-binary fixture matrix was **PASS** after adding an
authenticated health-first startup gate. It is bounded local fixture evidence,
not a supported-concurrency or performance claim. Earlier matrices that exposed
the intermittent local OpenCode startup condition remain retained below.

The benchmark script compiles the internal/opencoderuntime test package once
to a PID-unique binary, records that binary's SHA-256, executes every level
from the unchanged binary, and verifies its SHA-256 again afterward. Tasks
within a level run concurrently inside that one test process. This excludes
concurrent Go compilation from the timed levels. Each task has an independent
OpenCode process, private state root, Git workspace, loopback port, local TLS
provider, broker, and journals.

That report is .local/opencode-execute-benchmark-health-first.json,
SHA-256
45f9df48001ce297df46784c5d5a42c3891bd73dec8c5146e8e1ebfe120748be.
It binds test binary
84ae3095cecd5d87a29bbb7e835ff22d8b4492461a817a29389097340c66cec7,
pinned OpenCode executable
88d2fa691b2d9e32fde6d1039382a850ddf96fe49cd41683c6375fe1dc8ec2a5,
and fixture source bytes
6b6a0894e267912b78a5738f95b91aac9f651cfcf84bcfe491058115fa295759.
The last digest identifies the committed fixture text read by the agent. It is
not a hash of the harness source tree.

| Concurrent executions | Completed | Level elapsed | Raw per-task milliseconds |
| --- | ---: | ---: | --- |
| 1 | 4/4 | 28307.315 | 6941.354, 7160.733, 7173.204, 7032.023 |
| 2 | 4/4 | 16562.065 | 8183.773, 8276.986, 8038.449, 8285.079 |
| 4 | 4/4 | 9876.676 | 9628.587, 9876.676, 9789.591, 9623.853 |

Every completed task validates the exact final result, two provider requests,
one committed-source broker call, terminal provider seal, and offline recovery
without another provider request. The separate pre-intent cancellation check
passed and admitted no durable intent, process, broker call, or provider
request. No failed startup was retried or resent.

Earlier external-runner matrices launched a separate go test command per task.
One failure is conclusively attributable to concurrent compilation while the
shared checkout was changing: the compiler reported a missing imported package
file. That runner therefore did not isolate runtime latency. Other failures
from the same runner cannot be attributed to compilation: diagnostic runs used
distinct state/config paths, logged the OpenCode listener, observed zero
provider requests, and then received no HTTP response from /config before the
30-second bound. One sampled stuck OpenCode process was idle at about 525 MiB
private memory while its peers completed. The safe readiness receipt classified
later examples as deadline, with no HTTP response observed and eight polls in
about 29.9 seconds.

The predecessor compiled-once matrix reproduced intermittent readiness failures
at concurrency one and two while concurrency four completed. The condition was
not explained by a simple concurrency ceiling or resolved by changing the
runner. CPU saturation, a shared global lock, and a general four-process
capacity limit are not supported by the observations.

A later bounded concurrency-one diagnostic reproduced the condition in three
of four production `Execute` tasks. In every failed task, the pinned process
accepted TCP at about 701 ms, returned an authenticated JSON HTTP response from
`/global/health` at about 1.21 seconds, remained alive, and made zero provider
requests. The first connectable `/config` request returned no response headers
before the 29.87-second deadline. One live-process sample late in that wait had
no child process, 27 threads, about 308 MiB working set, about 512 MiB private
memory, and 4.27 seconds of accumulated CPU time. This rules out listener
startup, authentication, and a generally stalled HTTP server in those samples;
it localizes the failure to the instance-scoped request path without identifying
the blocked operation.

The exact pinned source makes that boundary concrete. `/global/health` returns
a constant health/version object without instance middleware. `/config` first
passes workspace routing and waits for `InstanceStore.load`; that resolves the
project and then bootstraps configuration, plugins, and the LSP, share, format,
VCS, snapshot, and project services before the small config handler can return.
The failed logs did not reach the `creating instance`, `fromDirectory`, or
`bootstrapping` markers. Therefore package installation and plugin loading are
possible downstream operations, but the observations do not establish either
as the cause. Sending the exact working directory as the typed `/config` query
also failed to resolve the condition: a bounded four-task A/B completed three
tasks and reproduced the same stall once. Ambient working-directory selection
is not supported as the cause.

The startup path now waits for an authenticated `/global/health` response with
the exact healthy/version shape before issuing the first instance-scoped
`/config` request. Health is only a liveness gate; the typed `/config` response
remains the sole configuration admission. The first health-first c1 A/B and the
subsequent compiled-once c1/c2/c4 matrix both completed every task. This supports
an early instance-route initialization race as the explanation for the sampled
failures. The internal OpenCode operation that previously failed to settle has
not been identified, so one passing matrix does not prove the upstream condition
is universally resolved.

A separate raw-process baseline used the same provider/tools configuration and
an actual dirty Git workspace. Four of four processes returned HTTP health
and complete config responses in 1.20-1.91 seconds with no proxy environment
entry and no provider request. This shows the typed config and Git workspace can
initialize successfully, but it does not isolate which additional production
`Execute` setup or runtime schedule triggers the intermittent state.

Retained diagnostic reports:

- .local/opencode-execute-benchmark.json, SHA-256
  a68dc569f1a5927f4e80131a4bf93e2e97ff044dbcf44f610b586cc4f19d824d
  (pre-health-gate compiled-once matrix: two of four completed at c1, two of
  four at c2, and four of four at c4).
- .local/opencode-execute-benchmark-c4-diagnostic.json, SHA-256
  2f2c8129fc9c72f30c1fa26f0bb4906f821957e81c8c74dea7c25e89cad14ff8
  (three of four completed; one listener/readiness timeout).
- .local/opencode-execute-benchmark-c4-debug.json, SHA-256
  8b6d85b564fda14a077628d64e613dd67bfe3dbc808e476ba738369d1dc19d6f
  (three of four completed; unique config-path logs retained).
- .local/opencode-execute-benchmark-compiled-wrong-cwd.json, SHA-256
  3b88df9e69c6fbb729b4998796a03e3aef1fe2dc5c4aa1e198f9355435bf4a94
  (invalid qualification attempt; compiled test binary launched from the wrong
  working directory, so every fixture failed before runtime startup).
- .local/opencode-execute-benchmark-legacy-unverified-source.json, SHA-256
  a2ed6ed4b36ea919015063723a71a81f39bc1049a7eb9b39409ab1f757322335
  (passing predecessor report whose source_bytes_sha256 label did not clearly
  distinguish fixture content from harness sources).
- .local/opencode-execute-readiness-c1.txt, SHA-256
  9b405493a940d6e08e51c5ee2c31a8685dc9e346231b349fddc29cb74fa1f781
  (one of four completed; exact TCP, health, config, process, and provider-call
  phase evidence for three failures).
- .local/opencode-readiness-phases-git.json, SHA-256
  cee5aa5d95e5b56f686803ae43e99e4bb1c080bc9dfa9c3df750f697e3827281
  (four-process raw baseline with the exact typed config and dirty Git fixture).
- .local/opencode-execute-readiness-c1-explicit-directory.txt, SHA-256
  63ea1a5a9c7429df98233f5bf1bdfb670dcca773a94659e6b079def4b0e4d7a5
  (three of four completed; exact-directory startup alone reproduced one
  instance-scoped config stall).
- .local/opencode-execute-readiness-c1-health-first.txt, SHA-256
  5d852b76a2be413de6a6033e5e1e237a321963727585dae3ec8fd974ccdf25cb
  (four of four completed in the first health-first bounded A/B).

Host load was uncontrolled. These are raw wall-clock samples from a scripted
local TLS provider, not external provider latency, production capacity, agent
answer quality, or model-quality evidence. The sample is too small for tail
percentiles.
