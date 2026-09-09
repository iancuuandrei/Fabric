# 0003: Durable intent before effect

Status: ACCEPTED
Date: 2026-09-06

Storage amendment: [ADR 0006](0006-sqlite-journal.md), accepted 2026-09-08,
supersedes the JSONL-only backend choice. Intent/receipt and UNKNOWN semantics
remain authoritative.

## Context and problem

A process can stop after an effect occurs but before its result is recorded.
Repeating that operation can create duplicate commits, pushes or pull requests.

## Decision and rationale

Persist an exact authorized intent, perform the effect, observe actual state,
then persist a receipt. An unresolved intent is UNKNOWN and blocks automatic
retry. Reconciliation is a separate operation requiring observed evidence.
Use canonical JSONL with a SHA-256 hash chain and exclusive journal access.
Replay validates both the chain and semantic transitions.

## Alternatives and consequences

A database adds operational machinery before a demonstrated need. Blind retries
and success inferred from process exit alone are rejected. JSONL is transparent
but needs strict partial-write handling. A torn tail is corruption; do not
silently truncate it or infer that the effect never happened.

## Compatibility and validation

Journal v1 rejects other versions. Hashes detect modification, not authorship:
an attacker able to replace all local state can rewrite the chain. Tests cover
tampering, interrupted records, concurrent writers and unresolved intents.

## References

- [Run journal](../specifications/run-journal.md)
- [State machine](../specifications/state-machine.md)
