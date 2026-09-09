# Local RI artifact publication

The Go `ri.Store` handles opaque snapshot bytes in an existing dedicated state
directory. Rust validation must precede publication and follows artifact reads.
Store hashing uses the exact RI snapshot domain. It does not interpret graphs or
decide whether producer coverage is truthful.

`Publish` validates the supplied content identity, exclusively creates
`ID.pending`, writes and synchronizes its bytes, then hard-links it to `ID.jsonl`.
Link creation cannot replace an existing name. Successful publication removes
the staging name and verifies the final bytes. An existing final artifact is
verified and reused without replacement. Existing corrupt bytes are never fixed
by overwriting them.

A pending name blocks normal publication and store reads. `Reconcile` is an
explicit recovery effect: complete staging bytes can be linked, or a final name
already linked to the same staging file can be finalized. Partial staging and an
independently created destination are retained and rejected. The caller must
journal intent before publication and separately authorize/journal reconciliation.
These library effects are not yet wired into the run controller's effect journal.

The state directory and relative paths pass rooted path checks. Read bytes are
limited to 64 MiB and checked against their expected identity. Hard-link support
is required; unsupported filesystems return an error without an overwrite
fallback. File synchronization is executed, but power-loss durability of directory
entries and protection against a hostile process modifying the store are not
qualified. The content hash is rechecked on reads; filesystem permissions alone
are not used as evidence of immutability.

Executed Windows tests cover reuse preserving file identity, existing corruption,
partial/staged/linked/conflicting recovery states and concurrent publishers. The
actual Go-to-Rust test now publishes through this store before Rust validation.
The changed-byte rejection remains exercised after publication.
