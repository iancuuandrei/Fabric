# Run journal v1

Scope: durable ordered local evidence. Each newline-terminated canonical JSON
record contains version, sequence, previous hash, kind, payload and hash. The
hash is SHA-256 over domain `harness.event.v1` and the record without `hash`.
Sequence starts at 1; previous hash starts as 64 zeroes. Payload is an object.
Each record MUST be at most 1 MiB including newline; a journal at most 64 MiB.

For retained JSONL files, append MUST acquire an exclusive lock, replay existing bytes, validate the new
record, write the full record and sync before success. A process-local mutex
alone is insufficient. Exclusive lock-file creation provides a conservative
cross-process lease; a crashed owner leaves a lock requiring operator review.
Automatic stale-lock stealing is forbidden. Readers reject a journal locked by
a writer. A torn tail, version mismatch, malformed record or chain mismatch is
an error. Replay never repairs or truncates input.

New journals use pure-Go SQLite with the same v1 event envelope and hash domain.
Storage is detected from the SQLite file header, never the filename extension;
existing JSONL journals continue as JSONL without implicit migration. SQLite
uses immediate transactions, FULL synchronization and WAL, with contiguous
sequence/previous-hash constraints and append-only event rows. Structural and
controller semantic validation precede transaction commit. Commit errors require
readback; they do not authorize another external effect.

Missing-path reads create nothing. Existing SQLite inspection uses a read-only
connection, validates the schema and complete event chain, and does not initialize
or repair storage. First creation publishes a closed database through an atomic
link only after verifying no required WAL/SHM sidecar remains. Legacy lock markers
block legacy or missing-path admission; they are not SQLite transaction locks.
Caller contexts bound database operations. JSONL import is explicit, validates the
whole history and only targets an empty database; original files remain intact.

`harness inspect RUN --export-jsonl` validates the run/repository binding and
exports canonical newline-terminated events from one consistent snapshot.
The 64 MiB bound applies to the canonical exported history, not physical SQLite
page overhead. See [ADR 0006](../adr/0006-sqlite-journal.md).

The journal package validates structural integrity. The controller MUST also
replay semantic invariants before resuming effects. Arbitrary well-hashed events
are not authorization. Empty journals carry no run authority. Append failure
after a write has begun is uncertain: callers MUST replay, not blindly retry.

An intent without a confirmed receipt remains UNKNOWN. No external action is
performed by journal append. Hash chaining is tamper evidence, not a signature
or protection from a hostile local administrator. Runtime transcript text
cannot change permissions. v1 rejects historical or future versions.

Example order: `run.created`, `plan.recorded`, `plan.approved`; the controller
specification defines admitted payloads and transitions.
