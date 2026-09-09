# Durable context over MCP

The controller owns model-access admission, the invocation, the repository
identity, candidate leases, and process lifetime. MCP transports read-only
context requests; its bearer is not authority to change files or approve work.

## Binding and recording

`contextbroker.Binding` fixes the invocation ID, immutable source identity,
optional candidate fingerprint, catalog ID, and call/byte limits. The broker
copies these inputs and persists the binding before accepting calls. Its static
catalog comes from `sourcetools` and `candidatetools`.

Each accepted call writes a request before domain I/O. It writes a canonical
response before returning successfully. Replay retains both requests and
responses, rejects substitution and duplicate call IDs, and permits no successor
to a pending request except its matching response. Reopening a journal never
executes a pending request. A failed append or oversized response can therefore
leave the call unresolved. Closing the in-process broker stops local admission
even when durable closure fails.

The broker mutex serializes callers in one process. Journal append locks protect
individual transitions. Neither mechanism establishes exclusive cross-process
ownership for an entire invocation; the controller must retain that ownership
and any candidate lease.

## Identity carried into OpenCode

The transport normalizes JSON-RPC request IDs without equating numeric and
string IDs. `contextmcp` hashes their typed JSON bytes into a bounded broker call
ID. Repeated use of the same normalized ID is rejected. A different ID represents
a new request, even when its tool and arguments are identical. Reconnects and
client retries must not manufacture new IDs to retry uncertain calls.

OpenCode's provider tool-call ID is a separate identity. The MCP result text
therefore contains the complete canonical `contextbroker.Response`, including
binding, invocation, request and broker-call IDs, success status, and content.
A tool-turn validator must compare that envelope with the durable response and
its corresponding request. Matching only tool name, arguments and source bytes
is insufficient when legitimate calls repeat.

Domain failure responses retain their envelope and set MCP `isError`. Such a
response does not by itself qualify an OpenCode tool turn: the runtime can
transform error output. Transcript admission must reject any missing or altered
receipt rather than infer it from similar text.

## Transport bounds

The server binds `127.0.0.1` on an already-owned ephemeral port. It validates the
exact Host, endpoint, bearer, permitted Origin, protocol header and whole JSON
message before callbacks. Duplicate keys and trailing JSON values are rejected.
Its catalog is evaluated once, copied, hashed and advertised without resources,
prompts, server instructions, or mutable-catalog notifications.

`contextmcp` allows one concurrent call, a 64 KiB request body, a 15-second call
deadline and a 1 MiB response envelope. It rejects a broker configured above
`MaxContentBytes` (86,016 bytes per response) before creating the server. The
capacity reserves 16 KiB for receipt/framing metadata and allows a conservative
12-fold expansion of canonical content across JSON-escaped MCP text and
structured representations. This includes the encoder's HTML-sensitive escaping.
The broker's cumulative response and call limits remain independently enforced.

Composite context/agent listeners retain one active callback. Their optional
`serial-fifo-v1` admission policy accepts a bounded number of waiting HTTP calls;
it does not execute additional callbacks concurrently. The controller selects
32 waiting calls for fresh scheduled composite explorers and records this limit
in the version 2 tool-receipt binding before runtime dispatch. Version 1 bindings
retain their original zero-queue behavior on recovery. A changed policy or limit
cannot be substituted into an existing invocation.

The wait deadline includes queue time. FIFO means validated server admission
order, not provider output-array order. A full queue rejects the request before
tool execution. Queued request IDs participate in duplicate detection, and a
queued cancellation or listener shutdown must not start its callback or write a
tool-call intent. Admission to the waiting queue is ephemeral; only starting the
callback crosses the durable tool-receipt boundary. The same HTTP call waits
for its slot: the transport does not retry or manufacture a replacement ID.

Provider argument SHA-256 and the domain-separated MCP argument digest describe
the same canonical arguments in separate identity domains. Composite transcript
evidence retains both; provider comparison must use the provider digest, while
MCP receipt matching must use the MCP digest. Provider names also retain their
exact `engorch_` namespace at that boundary.

## Usage projection

The pinned runtime reports non-cached input, non-reasoning output, reasoning,
cache reads and cache writes separately. Budget projection must include the
cache components in input and reasoning in output, with checked addition.
`ToolTurnTokens.InclusiveTokens` performs that projection without changing the
raw component observations. Step-finish usage repeats assistant usage and must
not be added a second time.

OpenCode also normalizes missing upstream SDK usage to zero. A host-reported zero
therefore cannot independently prove zero provider usage. A provider gateway or
another qualified upstream receipt must establish that provenance before these
counts are used as authenticated provider accounting. The exact transformation
is in the [pinned runtime's usage implementation](https://github.com/anomalyco/opencode/blob/16747470f976aca3d362ad730bcd3fe82ecc2c9a/packages/opencode/src/session/session.ts#L338).

## Observation and delivery

An admitted initialize or catalog-list observation proves that protocol operation
reached the owned listener. OpenCode's connected status alone does not prove the
catalog or a tool execution. A durable response proves recorded domain output,
not that the model received it. Cancellation after recording, credential-output
filtering, and HTTP write failure can leave delivery unknown. These cases never
authorize repeating domain execution.

Before releasing the candidate lease, the controller must stop new admissions,
settle callbacks, confirm listener/handler shutdown, reap its OpenCode process,
and inspect durable broker closure. A timeout from shutdown is not confirmation
that callbacks have exited. Transcript snapshots and an idle status are not
standalone proof of terminal process state.

## Executed scope

Local tests exercise real Git reads through the durable broker and actual HTTP
transport, exact receipt output, typed ID separation, duplicate rejection,
domain errors, JSON ambiguity rejection and response-escaping capacity. Race
tests cover the integrated bridge. The pinned OpenCode bootstrap probe separately
observes initialization, catalog listing and exact configuration/status readback.
A separate synthetic provider probe made pinned OpenCode execute `source_read`
once and return the full durable receipt in its next provider request, preserving
committed bytes despite a dirty source checkout. A candidate probe separately
read modified bytes from a leased worktree. Both actual transcripts passed the
tool-turn decoder through production synchronous dispatch, and offline recovery
replayed the exact observation without another provider request. Both owned
fixtures also passed production terminal sealing and journal-only terminal
recovery, preserving one broker call and two provider requests. Their exact
synthetic SSE responses passed the provider stream parser with explicit usage.
Constructor ownership checks now bind the exact broker pointer, listener,
credential digests and verified process launch; the final live probes passed
with these checks and context-bounded root reaping. Synchronous journal writes
do not carry a hard disk-I/O deadline, and OS containment is not claimed.
These results do not qualify the controller workflow or external models;
current maturity is tracked in [the status ledger](../evaluation/status.md).
