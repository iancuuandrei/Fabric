# RI subprocess protocol v1

## Persistent framing

`engorch-ri --stdio-stream` accepts sequential frames without waiting for EOF.
Each frame is a four-byte big-endian length followed by canonical JSON, bounded
to 1 MiB. Requests are `{id, request}` with IDs starting at one and increasing
by one, up to 10,000 per process. Responses add the same `id` to the v1 result
envelope. Malformed frames or sequence violations terminate the process;
correlated operation errors leave it available for the next request. EOF is
clean only between frames. No request is automatically retried.

Go `Client.OpenStream` verifies the executable pin and owns the child lifetime.
`Stream.Call` serializes callers, validates response correlation and canonical
schema, and bounds each call to 30 seconds including admission wait. Cancellation
during IO or a protocol fault closes pipes and cancels/reaps the child. Cancelling
a caller waiting for admission does not cancel another caller's active request.
The owner must call `Close`; its lifetime context also cancels the child. Stderr
is bounded to 64 KiB across the session. Requests that fail local serialization
are not dispatched. This primitive does not grant effects authority.

Actual Rust and Go-to-Rust tests cover multiple requests before EOF, a correlated
operation failure followed by success, closed-session rejection and lifetime
cancellation/reaping. Malformed frame and response tests reject invalid lengths,
truncation, duplicate/out-of-order IDs and invalid result envelopes. The lexical
handler retains at most one disk index, keyed by the complete base artifact paths,
manifest/source identities and build descriptor/identity. A changed key evicts
the old index before opening the replacement. Reuse retains mmap readers and
decoded lookup/path metadata, but compares the manifest one canonical record at
a time against retained metadata and hashes each shard file
before searching; failed shard verification evicts the entry. Selected source
bytes remain hash/blob verified per request. Changed overlays are rebuilt in
memory per request. Removing repeated artifact IO requires stronger immutable
storage ownership and separate qualification; no performance SLA is claimed.

Manifest identity calculation sorts borrowed file references, without cloning
all path/blob/hash strings. Reuse does not allocate a second complete manifest.
Both changes preserve the record ordering, exact source and content identities;
the post-change release memory footprint still requires measurement.

`Stream.SearchLexical` and `Stream.SearchLexicalOverlay` share the one-shot
client's complete admission and result projection. The actual Go-to-Rust fixture
builds a two-file index, pages across matches, applies a replacement and deletion,
rejects a base cursor in the overlay scope, and detects source corruption between
requests. Runtime host dispatch for planner, writer, reviewer and explorer now
owns a `codexruntime.ToolSession`: it lazily opens one reader from the journaled
lexical binding, serializes durable broker calls, rejects binding substitution,
and cancels/reaps the reader when the host execution ends. Failed launch or
transport never triggers automatic replacement. Direct `Adapter.HandleTool`
remains a one-shot entry point for callers without an owned host session.
The actual Rust broker fixture verifies reuse across successful and rejected
tool requests, closed-session rejection and no replacement after reader shutdown.
Provider correlation in this fixture is synthetic; live model tool use with
persistent lexical transport has not yet been qualified.

## One-shot transport

`engorch-ri --stdio` reads one canonical JSON request from stdin and exits after
one canonical JSON response. Snapshot queries start no source producer. The
explicit `scip_import` operation creates a new staging artifact; queries make no
repository edits. The explicit `rust_types` operation parses supplied immutable
source text; see [structural extraction](ri-structural.md). Snapshot queries never
trigger it. This is the internal Go/Rust transport; the full agent-facing `harness ri`
command family remains incomplete.

Single-snapshot operations require an absolute `path`, expected `snapshot_id` and complete
`source` binding. For example, `operation` can be `status` or `neighbors`. Status reports source,
snapshot ID and node/edge/coverage counts. Neighbors additionally requires `query`
and nullable `after`, using the [query contract](ri-graph.md). Every request loads
and verifies the exact artifact, including its expected source and hash.

The response has `version: 1` and `ok`. Success includes an object `result` and
process exit zero; failure includes an `error` string and exit two. Unknown,
omitted or noncanonical fields are rejected. Stdin/stdout are limited to 1 MiB;
snapshot bytes to 64 MiB. The reader rejects a non-regular final path and Windows
reparse points at that path. It does not claim ancestor-path or operating-system
confinement. Artifact hash verification remains mandatory after reading.

The Go `internal/ri` client requires an absolute executable and its SHA-256 pin,
checks the executable before launch, uses direct argv and a finite environment,
and bounds execution to 30 seconds. Stderr is limited to 64 KiB; excess output
cancels the process. No automatic retries occur. Only canonical successful v1
object results are returned to the caller. This client does not install tooling,
build Rust or admit results into a run journal by itself.

Executed tests use the actual Rust binary, rather than a substitute process:
Rust reads a disk artifact and rejects changed bytes; Go creates canonical
snapshot bytes and obtains verified status from Rust, then observes rejection
after mutation. `scripts/check.ps1` builds the binary and repeats the Go/Rust test
with `ENGORCH_RI_BINARY` set to Cargo's reported target directory. Ordinary Go
tests explicitly skip that integration test when the variable is absent.

The [local artifact store](ri-store.md) now provides publication and explicit
recovery primitives. Controller journal integration, full RI commands and
producer execution remain unfinished parts of the requested implementation.

The occurrences operation accepts path, snapshot_id, source, query and nullable after. Its query contains symbol, producer, definitions (boolean) and limit (1..128). It returns direct semantic occurrence records, exact snapshot/query identities, a nullable continuation and absence_proven:false. Cursor identity includes role selection and page size. Unknown roles are excluded. Relationship-expanded definitions/references are not yet implemented. The query walks only the symbol index and retains at most limit+1 records.

The scip_symbol operation accepts the artifact path/identity/source plus producer, exact symbol spelling and nullable document scope. It returns the matching node or null and absence_proven:false. Local symbols require a document; lookup uses their deterministic scoped identity. Occurrence pagination now has an executed real subprocess test covering continuation and role-crossed cursor rejection.

The path operation accepts artifact identity/source and a query with from, to, relation, direction, producer, max_depth (1..128), max_edges (1..100000). Deterministic BFS returns observed path edges, found, truncated, examined_edges and absence_proven:false. The edge budget counts all examined adjacency entries, including filtered producer/relation entries. Depth exhaustion conservatively marks truncation when adjacency remains. Cycles are visited once; reverse traversal retains original edge provenance. The response includes snapshot identity and the exact query.

The coverage operation accepts artifact path/identity/source and node/relation/direction. It returns one declaration per registered producer (at most 64), retaining null for no assertion and UNKNOWN for explicit unknown coverage. It does not enumerate edges or claim absence. Actual subprocess tests exercise coverage and bounded path requests.

Go Client.Inspect and Client.Coverage now decode typed results and verify exact snapshot/source or query scope, count bounds, producer ordering and completeness vocabulary. Actual Go-to-Rust tests execute both methods. Response scope validation does not independently authenticate the producer registry; that remains part of the pinned reader and snapshot validation.

CLI RI executable and snapshot paths resolve relative to the selected --root repository when not absolute. The executed CLI integration uses a separate process working directory and verifies root-relative artifact resolution, direct/reverse dependencies, a two-edge path, depth exhaustion and rejection after a new commit.
The locate operation accepts artifact path/identity/source plus query {path, offset, producer, limit} and nullable after. Offset is an exact byte position. All overlapping observations are paged in stable ID order; nonempty intervals exclude their end, empty intervals match exactly. It makes no symbol-ranking or absence claim. Out-of-source offsets without observations return empty; file length is not presently stored in the graph.
The changed operation takes before_path/before_id/before_source and after_path/after_id/after_source. Both artifacts are independently validated before their manifest inputs are compared. Output binds both IDs and separates source input changes from producer changes. Common repository authority remains a controller responsibility. Large unpaged change sets exceeding the protocol envelope fail explicitly. The changed_page operation additionally accepts limit and nullable cursor, returning at most 128 source deltas and at most 512 KiB of serialized source records. This reserves transport space for producer changes and cursor metadata. Its cursor binds both ordered artifact IDs, page limit and last producer/path. CLI changed accepts optional LIMIT [CURSOR]; a real CLI-to-Rust test covers two pages and changed-limit rejection. Paging limits the returned projection; the reader still computes the complete manifest comparison for each request.

The scip_import operation takes a nested import request (index_path, manifest, source, producer, project_root, sources, policy) and an absolute output_path. Policy is strict or scip_go027. After complete admission it creates output_path exclusively, writes and syncs the snapshot, and returns snapshot_id, bytes, source and output_path. It never replaces an existing output or publishes a content-addressed name. Failed or interrupted writes can leave staging bytes: the controller must journal intent first and reconcile explicitly, rather than retry blindly. This operation avoids the 1 MiB JSON response limit for snapshots up to 64 MiB. Parent-directory confinement and crash-durable directory metadata are not established by this Rust primitive. The actual process test verifies exact output and rejection of repeated staging; Go effect integration remains pending.

Go Client.Import now binds the request to a controller Repository Identity, checks explicit absolute paths and a new staging destination, validates the compact receipt, reads the staged file through os.Root, checks its exact snapshot hash/size and asks the pinned Rust reader to admit it again. It leaves errors and staging bytes for explicit caller reconciliation. The actual Go-to-Rust fixture exercises an empty semantic index, exact receipt and rejection of repeated staging and foreign source binding. This fixture uses synthetic repository identity; committed-source acquisition and journal ownership remain controller work.

ImportPlan now defines the immutable Go effect payload: version, pinned executable, repository identity, complete import request and output path. Its domain-separated hash is bound to a ri_import effects.Intent with run and plan identities. Substitution tests cover destination, executable hash, policy and index location; an intent without a receipt remains UNKNOWN. This is an integrity/approval contract, not proof of committed source acquisition or journal integration. Semantic request admission remains in Rust; controller replay and effect execution are still pending.

Controller events ri.import-intent and ri.import-observed now reconstruct RI import state. ExecuteRIImport persists exact authorized intent before dispatch, records verified staging only on successful client admission, and leaves failed execution UNKNOWN. Replay rejects repeated intent identities, foreign repository/destination observations and further events while import uncertainty remains (except import observations). The executed controller failure test confirms durable event ordering and retry rejection. Successful controller-to-producer qualification, an explicit reconciliation command, committed-source acquisition and content-addressed publication remain pending.

Reconciliation is now available through controller ReconcileRIImport. The read-only scip_import_expected protocol operation reconstructs a snapshot from the pinned inputs and returns its identity, size and source without writing output. Go ObserveImport verifies existing staging against that expected identity. Missing inputs, missing staging or mismatched bytes remain UNKNOWN; successful observation appends confirmation. Executed controller-to-Rust tests cover successful execution/replay, duplicate rejection, missing and partial staging without mutation, and completed staging whose observation was interrupted. This uses an empty index and synthetic repository identity; it is not real-producer or committed-source qualification. The standard check script now executes this controller integration with the built Rust binary. CLI reconciliation and content-addressed publication remain pending.

VerifyCommittedSources now validates the selected producer source registry against an observed exact historical Git commit and streams each regular blob through DigestSource. It compares raw SHA-256 values, enforces the 64 MiB aggregate source budget and returns sorted blob/digest evidence. The executed Git fixture modifies the checkout after commit: the committed digest passes and the dirty-file digest fails. This read-only preparation primitive is not yet invoked by ExecuteRIImport; materialization and durable source evidence admission remain pending.

ExecuteRIImport now calls VerifyCommittedSources before recording a new intent; ReconcileRIImport repeats the same check before admitting staging. The controller fixture now uses an observed real Git commit and a SCIP document for a committed source file, with a separate exact input copy and a dirty checkout. Execution and interrupted-result reconciliation pass without reading the dirty source as committed content. Input-copy creation is still performed by the fixture, not a controller materialization effect, and the semantic index remains synthetic rather than real-producer output.

MaterializeSources now provides a caller-owned filesystem primitive for exact input copies. It verifies committed source evidence first, requires new destinations in existing validated directories, copies regular Git blobs through os.Root with exclusive creation, syncs files and compares resulting digest/blob evidence. It never creates directories, overwrites files or removes partial output. Tests verify committed bytes despite a dirty checkout and reject repeated materialization. The controller must journal this separate write phase before invocation; that lifecycle integration remains pending.

ImportPlan now includes materialize_sources in its effect identity. When true, ExecuteRIImport records intent before invoking MaterializeSources, then starts the Rust import only if all copies succeed. Existing source destinations fail without overwrite; any failure after intent remains UNKNOWN. When false, the caller supplies existing source copies, which still pass committed-source and Rust input-hash validation. The actual controller fixture now creates its input copy through this journaled path, rather than manually. Source copying and staging are one compound effect; reconciliation observes the final exact result without completing partial writes. Real semantic producer execution and final artifact publication remain pending.

Local publication now has controller PrepareRIPublish/ExecuteRIPublish and ri.publish-intent/ri.publish-observed events. Exact authorization binds confirmed import, artifact receipt and store directory. PublishImport recomputes the imported staging identity, publishes through Store and verifies final Rust reader admission. Interrupted observations can be reconciled read-only with ReconcileRIPublish; missing or pending artifacts remain UNKNOWN. Completing pending hard links still requires a separate recovery action, which is not yet integrated in the controller. Actual controller tests cover publish/readback, duplicate rejection and missing-versus-completed publication reconciliation. This is local artifact publication only, unrelated to Git push or PR publication.

Pending publication recovery now has PrepareRIPublishRecovery/RecoverRIPublish and a distinct ri_publish_recovery intent. Replay binds recovery to the exact unresolved publication, rejects substitution/repetition, and journals authorization before Store.Reconcile can link/remove pending names. ReconcileRIPublish then verifies final output through Rust. The actual controller fixture covers pending-name recovery, rejection of the original publication approval for recovery, final confirmation and repeat rejection. Each publication currently permits one recovery attempt; further uncertain outcomes can be observed read-only but a new recovery-attempt identity is not yet supported.

Recovery attempts can now continue after a recorded observation leaves publication UNKNOWN. Each new recovery identity binds the original publication, previous recovery intent and current observation. Replay refuses another attempt while the previous recovery has no observation. The executed partial-staging test confirms a failed first attempt, a distinct second identity, stale-approval rejection and successful recovery after the fixture restores exact bytes. There is no automatic retry or repair of partial bytes.

RI lifecycle APIs are now exposed through the CLI: prepare-import/import, prepare-publish/publish and prepare-publication-recovery/recover-publication. Import plans are bounded JSON files; the prepare command returns the exact effect ID. Execution takes that ID and actor explicitly. Relative plan/store paths resolve against --root; journal/run repository binding is checked before dispatch. Generic reconcile routes unresolved RI publication/import to read-only observation. See the generated CLI reference for arguments. Dedicated end-to-end lifecycle CLI qualification remains pending; controller-to-Rust paths are tested.

The dedicated TestRILifecycleActualRustCLI now executes init/plan/approve, RI prepare-import/import and prepare-publish/publish against the actual Rust binary and an observed Git commit. It rejects wrong import approval and verifies stored artifact bytes. An interrupted publication fixture also exercises generic reconcile rejection for pending output, prepare-publication-recovery and recover-publication confirmation. The standard check script invokes this test with the built Rust binary. The semantic index in this CLI fixture is intentionally minimal and synthetic; real-producer qualification remains separate.

The Codex runtime now supports an optional immutable RIBinding, recorded as runtime.ri after source binding and before thread creation. Resume requires the same binding. Its ri_status tool accepts no artifact selection arguments and reads only the fixed snapshot through the pinned Rust client, with normal durable tool request/response receipts. Binding tests reject foreign sources, duplicate configuration and unbound RI calls. Controller selection of the published snapshot, actual broker-to-Rust qualification and semantic tool expansion remain pending.

TestActualRIBrokerStatus now executes the runtime tool broker against the real Rust binary and a fixed published fixture snapshot. It verifies source/snapshot identity in the durable tool response, pending-call completion, and failure after artifact bytes are modified. The standard check script runs this broker integration after building Rust. This uses synthetic provider correlation and does not constitute an authenticated model tool-use test.

The runtime catalog now also exposes ri_locate, ri_definition and ri_references when an RI binding is present. Each query uses the journaled snapshot and executable hash, an explicit producer, bounded limit and nullable query-bound cursor. Definition versus reference scope comes from the tool name and cannot be overridden in arguments. The actual Rust broker fixture covers empty observed pages without absence claims, forbidden snapshot/role overrides, oversized limits and foreign cursors. Populated semantic navigation is separately qualified through the actual producer CLI; authenticated model semantic navigation and controller binding selection remain pending.

SelectRuntimeRI and ri runtime-binding RUN now select the current confirmed publication from the controller journal, bind it to the exact import intent, check the store has no pending publication and revalidate artifact/source identity with the pinned Rust executable. They perform no indexer, import or recovery effects. A newer unmatched import or unresolved publication cannot silently replace the runtime binding. TestActualScipGoCLI executes real producer/import/publication, selects this binding, and uses the runtime broker to locate Greeting and retrieve its definition with the committed source hash; the reference query excludes the definition. Provider thread/call correlation in this test is synthetic, so authenticated model execution is still not claimed.

The status response also includes producers: the sorted producer IDs from the verified snapshot manifest (1–64 entries). Semantic tools require an exact ID from this registry; callers must not infer it from executable names. The Go client rejects missing, oversized, duplicate or unsorted registries. This field closes a discovery gap observed in authenticated writer/reviewer qualification.
