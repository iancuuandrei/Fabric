# 0004: Repository intelligence reports evidence

Status: ACCEPTED
Date: 2026-09-06

## Context and problem

Navigation results are useful but incomplete indexing can turn missing edges
into misleading absence claims or cause relevant source to be hidden.

## Decision and rationale

Rust RI will expose immutable commit/tree/producer/input-bound snapshots and
finite bounded queries. Canonical evidence distinguishes DECLARED, OBSERVED and
INFERRED plus precision and coverage. Absence requires complete coverage for
the exact relation and scope. Queries cannot silently rebuild snapshots.
SCIP is the semantic interchange; structural evidence stays explicitly weaker.

## Alternatives and consequences

Vector databases, PageRank task gates and mutable latest pointers are rejected.
They do not establish exact provenance. Initial content-addressed local storage
is preferable to graph infrastructure. This costs explicit build steps and
requires producer coverage metadata rather than convenient completeness claims.

## Compatibility and validation

RI protocol starts at v1, independently of runtime/journal versions. RI is a
later implementation phase after the Go kernel. Qualification must include
false-absence cases, cursor identity and Go/Rust serialization agreement.

## References

- [Language split](0001-language-split.md)
- [Donor mechanisms](../research/oss-mechanisms.md)
