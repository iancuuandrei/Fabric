# 0006: SQLite durability with canonical event semantics

Status: ACCEPTED
Date: 2026-09-08

## Context and problem

The user selected mature OSS durability rather than extending a custom JSONL
storage engine. Existing journal evidence and uncertain effects must survive
that change without altering canonical event identities.

## Decision and rationale

Use pinned modernc.org/sqlite for new journals through existing journal APIs.
Preserve canonical v1 payloads, hash chains and controller semantic replay.
Detect existing format by content, keep legacy JSONL behavior, and provide
read-only inspection plus exact canonical JSONL export. Do not silently migrate,
truncate, repair, replay effects or steal legacy locks.

SQLite supplies immediate transaction isolation, FULL durability and WAL recovery.
The journal retains application invariants, bounded histories and uncertain-commit
classification. Concurrent first creation publishes one complete closed database;
losing contenders revalidate the winner. Explicit imports require an empty target.

## Alternatives and consequences

Keeping JSONL as the default would preserve the bespoke durability implementation
the user explicitly asked to replace. Forced migration would risk existing
evidence. The dual-backend transition preserves compatibility but requires tests
for both paths. Filenames may still end in .jsonl for compatibility; export is
the transparent interchange format, and readers must not guess storage by suffix.

## Compatibility and validation

Tests exercise canonical round trips, schema rejection, concurrent first creation
and writers, validator mutation isolation, transaction rollback, cancellation and
uncertain commit readback. SQLite transactions do not prove external effects or
replace the controller's exact intent/receipt authority. Full application and
cross-platform qualification remain separate requirements.

## References

- [Run journal](../specifications/run-journal.md)
- [Driver provenance](../provenance/sqlite.md)
- [Original effect decision](0003-journal-effects.md)
