# Semantic producer execution

`ri.PrepareProducer` freezes the selected executable, arguments, finite environment,
directory, timeout and repository identity. It reuses the existing verification
process engine for executable hashing, cancellation and bounded output. A distinct
producer-plan hash binds that invocation to the expected binary index location.

`ri.ExecuteProducer` requires a new output path and performs one process invocation.
It accepts an artifact only after successful process-result validation, then hashes
a regular output of at most 64 MiB through a rooted reader. Existing output paths
are rejected. Errors can leave output bytes; there is no automatic retry or deletion.
Process success and exact artifact bytes do not establish semantic correctness.
SCIP admission remains mandatory before producing a queryable snapshot.

The caller must journal intent, acquire the execution directory and establish its
source contents before execution. The current runner binds a repository identity
but does not itself prove that the directory still matches that commit, isolate
network access, or pin dynamic dependencies. Controller producer effects and
source-directory qualification remain unfinished.

The opt-in `TestActualScipGoProducer`, selected with `ENGORCH_SCIP_GO_BINARY`, has
been executed with installed scip-go 0.2.7 and the local Go toolchain. It creates a
real committed Go module, executes indexing, observes a nonempty hashed artifact
and rejects a second invocation at the same output path. This test qualifies the
process adapter, not the complete producer-to-import controller pipeline.

The opt-in test now also passes real scip-go output through the Rust importer when ENGORCH_RI_BINARY is supplied. It constructs provenance from the pinned producer binary, exact index hash, explicit scip-go position policy and committed source digest. The executed run admitted 3 nodes and 2 occurrences, located Greeting in the original source and resolved its semantic definition. This proves producer-runner/importer/query interoperability on the small committed fixture. Producer execution is not yet journaled by the controller, and execution-directory immutability still needs qualification.

ProducerPlan.Intent now binds the complete frozen producer plan to run, approved plan and repository identities under the ri_producer effect kind. Tests verify authorization invalidation when output path, command arguments or timeout change, and UNKNOWN when no receipt exists. This supplies the exact journal authorization target; controller producer event replay and dispatch remain pending.

ValidateProducerResult now checks the complete persisted process envelope against the exact producer plan, then validates artifact destination, hash shape, size and successful process status. A successful process with no artifact is admissible incomplete evidence, not confirmation. The executed real-producer test rejects substituted plan/invocation identities, output path, size and hash while retaining this incomplete-evidence distinction. Raw-byte and SCIP checks remain separate from envelope validation.

Controller ri.producer-intent/ri.producer-observed events and ExecuteRIProducer are now implemented. Replay validates run/plan/repository/authorization, rejects repeated effects and confirms only a validated result with an artifact. Execution observes exact Git identity before journaling intent, then records process evidence when valid. The missing-tool test verifies durable ordering, retained NOT_RUN facts, UNKNOWN effect outcome and rejection of automatic retry or foreign observations. Successful real-producer controller qualification, directory integrity and explicit unknown-effect closure remain pending.

TestActualControllerScipGoProducer has now executed installed scip-go through ExecuteRIProducer on a real committed Go module. The test verifies confirmed journal state, exact index hash/size against disk, replay preservation and duplicate execution rejection. It uses the opt-in ENGORCH_SCIP_GO_BINARY setting and local Go toolchain. This establishes successful producer controller dispatch; it does not establish directory immutability or join producer and import authorization into one end-to-end controller pipeline.

ExecuteRIProducer now requires the admitted isolated writer worktree, acquires its lease and verifies pristine raw committed contents before dispatch and after process completion. The index output must be outside the source workspace. Journal observations retain before/after candidate identities; confirmation requires matching admitted observations and an artifact. Actual scip-go controller execution passes in this worktree; an extra source file causes preflight rejection without journal mutation. Before/after checks do not prove absence of transient changes during execution, prevent noncooperating writers, or provide OS sandboxing.

BindRIProducerImport now creates an import proposal from a confirmed journaled producer: it binds producer_intent_id, exact index path/hash, executable hash and producer:invocation input identity. It clones nested producer/input slices before updating provenance. Import replay validates those bindings whenever producer_intent_id is supplied; externally acquired imports can still omit that optional binding. The actual controller scip-go test now runs a BoundImport subtest through Rust admission, with foreign-index rejection and caller-proposal immutability checks. This establishes the small real producer-to-import controller path; broader producer coverage and runtime qualification remain unfinished.

CLI prepare-producer accepts a run, bounded Check JSON and output path, freezing the process in the admitted workspace and returning a preview with its effect ID. produce accepts that preview plus the explicit ID/actor and invokes the journaled controller. bind-import returns an import proposal linked to the confirmed producer. These commands are listed in the generated reference. Dedicated real-producer CLI qualification remains pending; real producer/controller/import tests already exist.

TestActualScipGoCLI has now executed the public CLI path using real scip-go and Rust binaries: committed module, approved run/worktree, prepare-producer, produce, bind-import, prepare-import and import. The resulting journal confirms producer and source-bound snapshot admission. This opt-in test requires ENGORCH_SCIP_GO_BINARY, ENGORCH_RI_BINARY and a usable Go toolchain in the frozen PATH. It covers a small module, not broad producer/language qualification.

A validated NOT_RUN producer result with no artifact and identical admitted before/after source observations now classifies the effect as NOT_APPLIED. This closes known nonexecution without claiming success. Missing observations, started failed processes or changed source observations remain UNKNOWN. The same effect ID is still never replayed; an explicitly authorized changed plan can proceed after NOT_APPLIED. The executed missing-tool test covers both duplicate rejection and a fresh authorized invocation.

CloseRIProducer and CLI ri close-producer now record an explicit operator attestation that an interrupted producer and descendants have stopped. Closure binds exact intent, actor, evidence and pristine source candidate; it retains UNKNOWN and never admits an artifact. Replay rejects late observations after closure and permits separately authorized new producer plans. Executed controller tests reject missing quiescence attestation and verify retained uncertainty plus fresh-plan progress. This mechanism does not detect or terminate workloads; the attestation is external evidence.

The real-producer CLI test now continues through prepare-publish/publish, ri locate and ri definition against the final stored snapshot. It verifies the Greeting definition and its exact committed-source SHA-256. The executed path therefore covers isolated workspace, producer, bound import, publication and useful semantic navigation on one small Go fixture. It does not establish broad graph completeness, performance or multi-language support.
