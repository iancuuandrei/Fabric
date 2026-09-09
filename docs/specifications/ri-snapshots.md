# RI snapshot bytes v1

A snapshot is canonical JSONL with one manifest, then nodes, edges, coverage and occurrence
records. Every record has `kind` and `value`. All records end with a newline.
Nodes and edges sort by ID; coverage sorts by node, relation, direction and
producer using the Rust enum declaration order. Producers sort by ID and inputs
by name; occurrences sort by occurrence ID. Each occurrence requires an exact
file node and a matching producer input named `source:<path>`. Its resolved symbol,
if present, must name a graph symbol. Unresolved occurrences retain null symbols.
Producer input names permit up to 4,103 bytes to accommodate bounded source paths.
These bindings validate recorded provenance, not source spelling independently;
producers must compare spelling and source hash against immutable source bytes.
Duplicate identities are errors. This ordering is part of format v1.

The manifest binds the controller repository identity digest, Git object format,
commit, tree and a producer registry. Each producer has a name, version, artifact
SHA-256 and named input SHA-256 digests. Graph evidence may refer only to registered
producers. These fields identify claimed provenance; artifact hashing does not
authenticate a producer or prove that it executed correctly.

Snapshot identity is lowercase SHA-256 of `harness.ri.snapshot.v1`, newline and
all exact artifact bytes. A reader requires the expected identity and source.
It rejects digest mismatch, incomplete tails, noncanonical records, wrong record
order, schema drift and invalid graph/provenance. It performs no Git, network,
indexing or filesystem effects. The [subprocess protocol](ri-process.md) now
reads disk artifacts through this validator. The [local store](ri-store.md)
provides publication primitives; controller effect integration and the full
agent-facing CLI remain pending.

Each record is at most 1 MiB; the complete artifact is at most 64 MiB. The current
builder validates a cloned graph before encoding, so peak memory exceeds artifact
size. No peak-memory or throughput qualification is claimed. The reader validates
order directly without reconstructing an encoded artifact.

Canonical JSON uses ASCII keys, sorted objects, exact UTF-8 text, safe integers,
depth at most 64 and no whitespace outside strings. Floats, negative zero,
duplicate keys and malformed surrogate escapes are rejected. Typed decoding
must reproduce the original canonical value, rejecting omitted nullable fields
as well as unknown fields. The shared fixture in `testdata/canonical-v1.json`
and its SHA-256 file are consumed by both executed Go and Rust tests. This fixture
proves its Unicode/control/integer case, not exhaustive protocol equivalence.

The implementation uses [serde_json](https://docs.rs/serde_json/1.0.151/serde_json/)
for JSON parsing and [sha2](https://docs.rs/sha2/0.11.0/sha2/) for SHA-256. The
canonical layer enforces the narrower harness format independently of JSON parsing.
Cargo.lock records the resolved direct and transitive dependency versions.
