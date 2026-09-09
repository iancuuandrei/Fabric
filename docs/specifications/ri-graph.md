# RI graph construction and coverage

The Rust `engorch-ri` crate validates graph input before publishing immutable
indexes. Nodes and edges have unique stable IDs. Every endpoint and coverage
node must exist. Paths are relative source paths. Duplicate coverage scopes
are errors, including equal duplicates; input order cannot choose a winner.

Construction sorts IDs, builds node/path indexes and forward/reverse adjacency.
Queries visit the queried node's degree, not all graph edges. The initial limits
are 100,000 nodes and 1,000,000 edges or coverage records. These are explicit
construction bounds, not measured performance claims.

Each edge retains DECLARED, OBSERVED or INFERRED quality. Coverage is keyed by
the exact node, relation, direction and producer. An empty result proves absence
only when that exact scope has explicit COMPLETE coverage. Missing coverage is
UNKNOWN; PARTIAL cannot prove absence. An unknown node is an error. Coverage
of references cannot establish coverage of calls, and outgoing coverage cannot
establish incoming coverage. Producer identities cannot substitute for each other.

The crate currently provides graph construction and scoped adjacency as library
operations. [Snapshot bytes and manifest validation](ri-snapshots.md) are now
implemented at library level. Source extraction and SCIP import
and the full agent-facing RI commands remain incomplete. The
enclosing snapshot layer must verify artifact bytes before any graph can become
runtime evidence. The current manifest validator binds the expected repository
identity digest, Git object format, commit and tree, and normalizes the producer
registry. Each producer declares a version, executable/source digest and uniquely
named input digests. Empty or duplicate registries fail. `BoundGraph` rejects
edges and coverage from unregistered producers. These checks validate provenance
structure; they do not authenticate or hash the claimed artifact bytes.

Identifier lookup uses a [CodeGraph adaptation](../../third_party/codegraph/README.md).
It retains all segments within a bounded input and never chooses task context.
The Rust tests cover Unicode/camel/acronym segmentation, false absence across
all coverage dimensions, stable order, reverse adjacency and malformed graphs.

Adjacency pagination is available through `query::Snapshot`, whose constructor
verifies the expected snapshot bytes/hash and source. Queries explicitly include
node, relation, direction, registered producer and page limit (1–128). A cursor
binds snapshot ID, a domain-separated hash of all query parameters and last edge
ID. A foreign snapshot or changed parameter rejects the cursor. Tokens are
canonical JSON integrity bindings, not authentication credentials.

Pages retain at most limit+1 matching edge references and scan only the queried
node's adjacency. Results remain sorted by edge ID. Coverage describes the full
query scope; an empty later page cannot turn previously observed relationships
into an absence claim. Pagination performs no reindexing or filesystem effects.
