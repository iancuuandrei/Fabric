# Lexical RI integration evaluation

Status: pinned dependency, exact-byte indexing, immutable disk shards, resident
and disk overlay primitives, and journaled CLI build/recovery have executed local
fixtures. Runtime tool integration, persistent stdio, large-artifact transport
and performance qualification remain pending. This is initial release scope.

Pinned upstream: [microsoft/tgrep e2007b52d2b8fe4176159d0da20c9ba4a46d5aab](https://github.com/microsoft/tgrep/tree/e2007b52d2b8fe4176159d0da20c9ba4a46d5aab).
The inspected LICENSE is MIT, copyright Microsoft Corporation. Preserve its full
notice for any copied or adapted source. Nine source/license files were retained
with SHA-256 hashes in the local research manifest.

The workspace exposes tgrep-core separately from the CLI. Its public modules
include query planning, disk reader, builder and live/hybrid indexes. Prefer a
pinned dependency if its boundaries support the required immutable model.
The inspected builder walks a filesystem root and applies ignore rules; this is
not directly equivalent to indexing the complete admitted Git blob manifest.
Evaluate a verified materialization adapter before deciding to fork the builder.
LiveIndex accepts supplied bytes, which is a promising seam for candidate overlays.

The planner exposes And, Or and MatchAll. Its presence is not proof of soundness
against our chosen matcher and encoding policy. Differential tests must compare
indexed results with full evaluation, including Unicode case folding, short
patterns, alternatives, optional branches, empty matches and deleted/modified
paths. Unsupported matcher syntax must be reported, not silently reinterpreted.

Required integration contracts:

- Immutable base identity binds repository, Git commit, blob manifest, index
  format, encoding and matcher profiles, and index content hashes.
- Overlay identity binds base, candidate fingerprint and changed-path digest;
  replacements and deletion tombstones shadow base entries before matching.
- Candidate filtering has zero false negatives relative to the exact matcher.
  Without a sound restriction, evaluate the full admitted scope.
- Match results carry source/blob identity and exact byte ranges; coverage and
  truncation remain explicit. Lexical absence is never semantic absence.
- Ordering and pagination bind the complete query/profile/snapshot identity.
- Use harness-owned bounded stdio; do not adopt a mutable global TCP server.
- Measure bounded build memory, cold/warm query time and overlay update cost
  against rg at 10k/100k/500k files. Report correctness and machine conditions;
  upstream speedups are not EngOrch results.

The implemented ingestion choice follows the builder exclusion and encoding
findings below. End-to-end memory and representative performance still require
measurement on the requested repository sizes.

The first implementation uses upstream query planning and posting lookup, with
Rust regex byte matching for final verification. MatchAll must be handled by our
adapter: upstream execute_plan returns an empty vector for this variant rather
than enumerating files. The adapter explicitly selects the full supplied universe,
including empty and short files. Unicode patterns, case-insensitive requests and
inline-flag regexes currently retain exact matching but bypass filtering pending
broader equivalence qualification. This is a performance limitation, not a change
to matcher semantics. Tests exercise 56 query/mode combinations across nine byte
sources against an actual upstream LiveIndex, plus explicit fallback and invalid
syntax checks. These finite fixtures are not a universal soundness proof.

Lexical scope validation now has exact raw-byte SHA-256/length admission and Git
blob identity verification for SHA-1 and SHA-256 (type/size header included).
Executed disposable Git repositories confirm these hashes against hash-object
--no-filters for empty, ASCII and binary/CRLF bytes. This verifies object identity,
not membership in the claimed commit tree; controller tree admission remains
required before using the scope as repository evidence.

Disk seam qualification: build_index_for_files offers an exact path list but
applies decode_for_index and intentionally omits binary files. Do not use that
pipeline for the exact-byte matcher profile. append_overlay_to_index accepts
caller-produced posting batches and streams existing disk postings into a new
output generation. A test executed five append generations via LiveIndex byte
extraction and IndexReader mmap readback; seven queries matched full evaluation,
including binary, empty, short and Unicode sources. This supports reuse of the
unmodified disk format with our own admitted byte ingestion. It does not yet prove
bounded end-to-end memory, atomic publication, integrity seals or build speed.
Repeated small appends can rewrite the base many times; qualify batching and
merge scheduling before a performance claim.

The current implementation avoids repeated base rewrites by building independent
shards from bounded LiveIndex batches, each appended to an empty reader. Every
input is checked against its committed Git object ID, raw SHA-256 and byte length.
Readback verifies shard hashes and exact path coverage. A single Git tree stream
and cat-file batch process supply the Go observation/materialization path;
uncommitted worktree bytes are excluded. The source-byte batch ceiling does not
bound total process RSS: posting structures and manifest metadata add overhead.

The Go controller now journals the exact plan and authorization before staging.
The final completion file is a recovery candidate. Recovery re-observes committed
scope, hashes every unique staged source and shard, and opens the pinned Rust
reader before appending confirmation. Missing or corrupt evidence remains
UNKNOWN without rebuilding. Current recovery admits at most 1 GiB per shard file.
The caller must retain exclusive immutable ownership of staging throughout use;
hash checks alone do not provide isolation against concurrent mutation.

The CLI exposes `ri prepare-lexical`, `ri lexical`, `ri lexical-ref`, `ri search`
and the shared `reconcile` command. An executed disposable Git/Rust CLI fixture
covered preview without staging writes, wrong-approval rejection without journal
changes, exact committed search despite dirty worktree content, and recovery after
loss of the completion observation. Reference output currently embeds manifest
metadata and is subject to the 1 MiB canonical command boundary; it is not yet the
large-repository reference format. See [lexical lifecycle](../reference/lexical.md)
for the local command sequence and remaining limits.

The finite Rust `lexical_search` request also accepts an optional `overlay`
descriptor: candidate fingerprint, changed manifest path/ID, changed source root
and deletion paths. Changed source bytes are admitted against a 64 MiB aggregate
ceiling before loading, then verified by the resident overlay builder. The field
is omitted for base-only requests. A subprocess test covers replacement with
empty bytes, deletion, creation, paginated exact hits, rejection of another
candidate's cursor, and preservation of the base view. This is a protocol seam;
the Go controller integration described below now binds those inputs for the CLI.
Runtime tool integration remains pending.

Go overlay admission captures the journal's current candidate under its workspace
lease, derives changed bytes and deletion paths against the committed manifest,
and checks the candidate again after reading. Preparation exposes an exact intent
ID; execution captures the inputs again, compares the plan, and journals intent
before fresh staging. Complete readback checks canonical manifest bytes and raw
source/native Git identities before confirmation. Recovery observes existing
artifacts without repeating writes. CLI export and `--overlay-run` search require
the confirmed overlay to match the current admitted candidate; search also binds
the base manifest. Executed fixtures cover wrong approvals, duplicate attempts,
lost observations, corrupt artifacts, binary changes, deletions, stale candidates
and replacement search through the actual Rust process. These are local fixtures.
Candidate capture still inherits the existing 4096-file workspace limit, so it
does not qualify the requested 500k-file candidate workflow.
