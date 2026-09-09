# RI occurrences and source coordinates

An occurrence records exact source path, source SHA-256, byte span, spelling,
producer and evidence quality. A nullable symbol identity distinguishes unresolved
text from a resolved symbol. Structural types convert to OBSERVED occurrences
with null symbols. IDs bind path, source hash, producer and the type observation.

`SourceText` indexes immutable UTF-8 line starts and hashes the exact source once.
Limits are 64 MiB and one million lines. Zero-based positions convert from explicit
UTF-8 bytes, UTF-16 code units or UTF-32 scalar counts. Invalid scalar boundaries,
out-of-line columns, negative/reversed ranges and unknown encodings are errors.
CRLF terminators are excluded from line content; empty trailing lines remain.

SCIP bindings are generated from the complete pinned schema in
`third_party/scip/scip.proto`. Binary decoding is bounded to 64 MiB and uses Prost;
it is not a streaming importer and decoded allocations can exceed input size.
Document admission checks legacy and typed ranges against exact supplied source
bytes, requires both representations to agree, validates enclosing intervals,
and preserves all seven declared role bits. Unspecified encoding, unknown role
bits and substituted nonempty embedded text are rejected.

`scip::admit` validates the manifest against an expected repository identity and
checks `scip:index` plus `source:<path>` input hashes against supplied bytes. It
requires matching tool name/version and project-root URI, UTF-8 source encoding,
unique portable document paths and explicit position encodings. It performs no
filesystem lookup through the index's root URI. Aggregate source input is limited
to 64 MiB, documents to 4,095 and occurrences to 100,000. The caller must establish
that supplied files are regular committed sources; hash agreement validates the
declared binding rather than authenticating the producer. The admitted result
retains complete decoded relationships and external-symbol metadata. Conversion
builds source-bound occurrences, scoped symbol nodes and declared graph edges.
No coverage completeness is inferred by admission.

Occurrence validation checks source SHA-256 and exact interval spelling. It does
not resolve symbols or validate producer registration by itself. Snapshot records
now validate producer/file-input bindings and maintain path/symbol indexes. Empty
symbol lookup does not prove absence. Occurrences now carry nullable SCIP role
bits: null denotes unknown structural roles, zero an explicit semantic reference,
and the definition bit denotes a producer-declared definition. All seven flags
survive snapshot serialization; unknown bits fail deserialization. Definition and
reference indexes exclude null roles and make no completeness claim. Executed tests
cover Unicode boundaries, CRLF, multiline spans, invalid encodings, line limits,
source substitution and structural occurrences that remain unresolved.

SCIP symbol enumeration covers document occurrences, symbol declarations, external symbols and all relationship targets. Stable IDs bind exact producer ID and unmodified symbol spelling, with document scope added for local symbols. Local identifiers follow the captured simple-identifier grammar and cannot occur in external scope. Global strings remain opaque: full descriptor grammar validation is not yet implemented. Enumeration is bounded to 100,000 unique symbols and does not infer edges or coverage.

SCIP relationship conversion emits separate DECLARED graph edges for REFERENCE_RELATED, IMPLEMENTS, TYPE_DEFINITION and DEFINITION_RELATED. Multiple flags produce multiple categories; identical declarations coalesce by their exact endpoints/category/producer. These navigation relationships are not ordinary source REFERENCES or DEFINES edges. Direction is retained as declared; reference-search expansion across both directions remains query-layer work. No completeness is inferred from false flags.

`Admitted::snapshot` now constructs file/symbol nodes, semantic occurrences, file definition/reference edges and declared relationship edges. It rechecks source hashes, coalesces exact duplicates and invokes the normal immutable snapshot validator/encoder. It emits no complete-coverage claims. Rich SCIP diagnostics/documentation and enclosing-range metadata remain in the hashed input index; navigation records currently retain spans and roles. Exact raw symbol spelling and producer metadata are retained on symbol nodes. The executed binary fixture covers two documents sharing one local ID, definition/reference separation, deterministic snapshot bytes and source substitution after admission. A real scip-go interoperability run is described below; broader indexer qualification remains pending.

SCIP symbol nodes now retain optional exact producer/symbol metadata. Snapshot admission recomputes the symbol ID and document scope from that metadata and rejects mismatches, even if the enclosing artifact hash has been recomputed. Non-SCIP nodes omit the field. The binary fixture verifies raw local spelling survives serialization and substituted spelling fails admission.

The explicit scip-go 0.2.7 compatibility entrypoint requires a scip:position-policy input hash and exact producer name/version. Captured upstream revision 2e9ff3c2603a85daabe125c9f20075ec52df0731 uses go/token byte columns in internal/visitors/visitors.go scipRange. Only unspecified position fields become UTF8; contradictory explicit encodings fail. The original index hash remains unchanged in provenance. Actual scip-go output over testdata/scip-go passed admission and snapshot readback with 21 occurrences, including Unicode definition/reference spelling. The opt-in scip_go_live test uses synthetic repository identity, so it qualifies producer/importer interoperability rather than controller committed-checkout provenance.

The Rust import module now accepts an explicit index path and a map from repository-relative source names to absolute disk paths. It bounds the index and aggregate source bytes separately to 64 MiB, rejects nonregular final paths and final Windows reparse points, and requires selected source names to be declared by the producer. Normal SCIP admission validates indexed source hashes and coordinates before snapshot construction. It does not read through the metadata URI, authenticate committed-file provenance, publish artifacts or journal effects. The executed disk fixture verifies snapshot readback, substituted index rejection, unbound source rejection and file/path bounds. Subprocess import transport and controller integration remain pending.
