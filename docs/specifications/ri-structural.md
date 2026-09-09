# Structural Rust type observations

`structural::rust_types` parses explicit type fields and return types from exact
UTF-8 bytes using tree-sitter 0.27.0 and tree-sitter-rust 0.24.2. It adapts the
[Graphify type walker](../../third_party/graphify/README.md) into iterative Rust.
The parser reads no working-tree files and launches no compiler or resolver.

Each observation retains the full qualified spelling, type/generic-argument role
and inclusive/exclusive UTF-8 byte offsets. Repeated spellings at different
positions remain separate. The response includes exact input SHA-256 and a
syntax-error flag. These are observed syntax occurrences, not resolved symbol
edges. Semantic coverage is always PARTIAL; empty results cannot prove absence.
Macros, imports, type aliases, conditional compilation and compiler name binding
require stronger producers. The extraction intentionally does not use a method
name blocklist to suppress unknown relationships.

Limits: 1 MiB input, 100,000 visited nodes, 50,000 observations and 4,096 bytes per
spelling. Parsing and traversal share a five-second cooperative deadline. Bound
failures return an error, not truncated evidence. Syntax errors may still return
observations with the explicit error flag and partial semantic coverage.

The internal subprocess operation `rust_types` takes `source_text` and expected
`source_sha256`. It rejects a hash mismatch before returning results. It is an
explicit extraction operation; snapshot status/neighbor queries never invoke it.
The caller must bind these input bytes and producer identity to the repository
snapshot before importing graph evidence. That graph import is not implemented yet.
Structural results can convert to [source-bound occurrences](ri-occurrences.md)
with explicit null symbol identities. These occurrences can be imported through
the snapshot builder after matching registered file and producer source inputs.

Tests execute the real grammar and subprocess, checking qualified generic types,
Unicode byte positions, repeated occurrences, syntax errors, source hash mismatch,
invalid UTF-8, input bounds and traversal exhaustion. This does not qualify a
complete structural repository index or semantic Rust analysis.
