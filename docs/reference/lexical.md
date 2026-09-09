# Local lexical index lifecycle

These commands index the committed source bound to an approved run. Build staging
is a durable governed effect: review the prepared plan and authorize its exact
intent ID. A failed attempt stays UNKNOWN until existing artifacts can be verified.

For a run already approved for implementation, prepare a plan with the absolute
RI executable, its SHA-256, a fresh staging directory, and batch ceilings:

```text
harness ri prepare-lexical RUN EXE EXE_SHA256 STAGE_ROOT 67108864 10000
```

Save the complete JSON output as `preview.json`. The preview contains `plan` and
`intent_id`; it observes committed files without creating staging. The byte
ceiling includes source bytes per batch, and each individual source must fit.
The file ceiling limits files per shard. Neither is a total process-memory cap.

Execute the exact approved preview, then obtain a verified search reference:

```text
harness ri lexical RUN preview.json INTENT_ID ACTOR
harness ri lexical-ref RUN
```

Save the complete reference JSON as `ref.json`. Search it with the same pinned
executable. Options precede the pattern; use `--` before a pattern starting with
a hyphen:

```text
harness ri search EXE EXE_SHA256 ref.json --fixed --limit 100 needle
harness ri search EXE EXE_SHA256 ref.json --regex --case-insensitive "foo.*retry"
harness ri search EXE EXE_SHA256 ref.json --fixed --path internal/ri --type go needle
```

Results include exact Git blob identities and half-open byte ranges, query and
manifest identities, candidate/searched counts, truncation and a continuation.
`--path P` restricts search to the exact repository-relative path `P` or files
below `P/`; it is case-sensitive and accepts only canonical `/`-separated paths.
`--type EXT` restricts search to a case-sensitive final filename suffix `.EXT`;
pass the extension without a leading dot. The two filters combine with AND.
They restrict both immutable base and candidate-overlay entries before hit limits
and continuation boundaries are evaluated. Replacements and tombstones shadow
base entries before filtering. Each non-empty filter participates in the query
identity, so a cursor cannot cross filter scopes. Empty filters retain the
unfiltered v1 query identity.

Output is always JSON; `--json` states that format explicitly. Candidate counts,
searched counts and `full_scan` are always returned from the executed Rust plan,
so `--explain` requests the already-present evidence without changing the result
shape. `full_scan` describes trigram planning; path/type filters still restrict
the admitted file scope when it is true.
Pass the complete `next` object as `--after CURSOR_JSON` with the same query.
The reference must match the current committed repository source. Dirty worktree
files do not replace committed bytes. Lexical absence is not semantic evidence.

If staging completed but its observation was lost, use:

```text
harness reconcile RUN
```

Recovery only observes existing staging. It checks committed scope, source and
shard hashes and reader path coverage before confirming. Missing or damaged
artifacts leave the effect UNKNOWN; the command does not repeat the build.

For a run with an admitted writer workspace and candidate, prepare an overlay:

```text
harness ri prepare-overlay RUN STAGE_ROOT
harness ri overlay RUN PREVIEW_JSON INTENT_ID ACTOR
harness ri overlay-ref RUN
harness ri search EXE EXE_SHA256 ref.json --overlay-run RUN --fixed needle
```

Save the complete prepared JSON for execution; authorize its displayed
`intent_id`. The controller derives changed bytes and deletions from the admitted
candidate, journals the intent before writing, and verifies every staged artifact.
`reconcile RUN` also recovers pending overlays without repeating writes. Export
and search selection recheck the current candidate and confirmed artifacts;
search additionally requires the base manifest identity from the overlay plan.
Results identify the candidate overlay and replacements shadow the base before
matching and pagination.

Configured Codex writer, reviewer and explorer executions now select confirmed
lexical artifacts before dispatch and record the complete binding in the runtime
journal. Their invocation identities include the selected lexical scope. The
`ri_search` tool accepts only query options and pagination, with at most 100 hits;
models cannot supply artifact paths. Runtime responses are journaled before return.
If an overlay exists but no longer matches the admitted candidate, prepare and
materialize a fresh overlay before another role invocation. The controller refuses
to reuse the stale overlay. Base-only scope remains explicit when no overlay was
selected. Local fixtures exercised the real Rust broker and role selection;
actual provider/model execution with lexical tools remains unqualified.

Current qualification limits: reference and receipt transport retain a 1 MiB
canonical message boundary; recovery accepts shard files up to 1 GiB. Large-repository transport and
10k/100k/500k comparative benchmarks remain unfinished. Controller-owned immutable
staging is required throughout observation and search. These commands have local
fixture evidence, not a production or cross-platform performance qualification.
# Compact references and runtime records

`ri lexical-ref RUN` now emits a compact index descriptor: manifest path,
identity, source and file count, plus source/index directories and build receipt.
`ri search` accepts this descriptor and the older inline reference. It binds the
current committed source before hydrating a compact manifest, then validates
canonical JSONL records and exact identity before invoking Rust. Build receipts
remain inline and therefore retain the per-record size ceiling.

New runtime invocations emit `runtime.lexical-record` before provider dispatch.
Replay validates metadata without reading artifact files; lexical tool calls
hydrate and validate the referenced manifests. Legacy `runtime.lexical` events
remain readable, but a journal cannot mix or repeat the two binding events.
Continuation and writer/reviewer/explorer result checks compare the controller's
selected binding against either representation. These changes remove the inline
file-list limit from these paths; they do not qualify the complete controller
lifecycle or candidate capture at 500k files.
