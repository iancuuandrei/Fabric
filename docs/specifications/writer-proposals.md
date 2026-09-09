# Writer proposal boundary

Optional `writer`, `explorer` and `reviewer` profiles use explicit runtime/provider/model/effort/role fields. Names must match roles, and `Config.Route` never borrows the planner model. Codex roles require a pinned host. Absent optional profiles are omitted from canonical configuration JSON.

`PrepareWriterInvocation` binds the configured writer, approved plan, objective, run and admitted candidate. The required reply is strict JSON:

```json
{"candidate_id":"exact candidate digest","changes":[{"path":"file.txt","before_hash":null,"content_base64":"aGVsbG8K","executable":false}]}
```

This example creates `hello` followed by a newline; the candidate digest must match the actual input. Changes must be sorted and unique. An absent before hash means creation; absent content means deletion. Binary contents, portable paths, exact existing hashes and executable modes use the regular file-effect contract.

`PrepareWriterFiles` treats model output as untrusted, rejecting routing/input substitution, stale candidates and invalid output. It prepares the normal leased file proposal and checks that preparation observed the invocation's candidate. `RecordWriterProposal` records the invocation, result and prepared effect; replay reconstructs these bindings and rejects duplicate invocation proposals. `ApplyFiles` requires separate authorization for the exact effect identity.

## Runtime and provenance

`RunWriter` creates a private host per invocation and records host intent, readiness and observation. A workspace lease spans dispatch and before/after candidate fingerprints. The adapter executes or resumes its durable runtime; source, candidate, thread configuration, requested/observed model and result must match before admission. Host closure precedes lease release.

`writer.runtime-observed` binds invocation, thread, turn, runtime journal head and result hash. `InspectWithHead` obtains state and head from one validated read. Recovery compares an existing receipt rather than replacing it. Codex proposals require this receipt and matching host/model evidence; synthetic output alone cannot establish provider execution. Existing proposals do not authorize dispatching the invocation again.

## Context

`candidate_list` and `candidate_read` expose current admitted files with exact hashes and bounded pagination. Reads fingerprint the candidate before and after, reject protected or escaping paths, and return bounded bytes, optional UTF-8, full-file identity and continuation offsets. Source tools expose only the committed base. Modified candidates are supported; unrelated drift fails.

When verification exists, input includes required check names, tested candidate, verification plan, pending/closure facts, observed results and a hash of the full verification state. Diagnostic excerpts are limited to 384 UTF-8-safe bytes per stdout/stderr/error field with explicit shortening metadata. Earlier tested candidate identities remain visible. No excerpt implies an unobserved PASS.

After a review requests changes, input also includes the review invocation, reviewed candidate, verification plan, decision and bounded findings, plus a hash of the complete record. Diagnostics and findings are untrusted evidence and grant no effect authority.

A confirmed RI publication adds an optional `ri` input bound to its snapshot, source and pinned Rust executable. This changes invocation identity. The scope is explicitly `base_commit`: current changes must be inspected through candidate tools. Dispatch revalidates the publication store and executable, supplies RI tools to the runtime and verifies the exact binding before accepting a result. Replay reconstructs the binding from journal authority without filesystem reads. Without a publication, the optional field is omitted. RI observations establish neither relevance nor permission, and missing edges require coverage inspection.

## CLI and executed scope

`prepare-writer RUN` prints the invocation. `write RUN` executes or resumes and records a proposal; it does not apply it. Opt-in authenticated CLI fixtures have executed creation, modified-candidate tool reads and a failing verification → writer repair → separately authorized application → passing verification round. Real Git tests cover stale state and substituted provenance rejection.

The RI context and invocation identity tests pass with the actual Rust importer and publication. Authenticated writer/reviewer RI tool use also passed on a real SCIP/Rust fixture. A bounded authenticated negative-review repair loop also passes. Cross-host qualification, broad task quality and context performance remain unqualified. See the [evaluation status](../evaluation/status.md) for evidence scope.
