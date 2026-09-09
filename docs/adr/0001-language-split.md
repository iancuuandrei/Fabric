# 0001: Go control plane and Rust repository intelligence

Status: ACCEPTED
Date: 2026-09-06

## Context and problem

The project needs lightweight process orchestration and a separate efficient
repository evidence engine. Copying LexAI's Python implementation would preserve
private complexity and would not satisfy the standalone architecture.

## Decision and rationale

Go owns the CLI, deterministic controller, runtime adapters, Git/worktrees,
effects and verification. Rust owns immutable repository snapshots, indexing,
semantic ingestion and queries. Python is limited to evaluation/development
utilities. The initial process boundary is bounded versioned canonical JSON.
Each responsibility has one owner; Go must not reimplement Rust graph semantics.

## Alternatives and consequences

A single Go implementation is simpler operationally but does not meet the
requested separation of high-throughput RI. An all-Rust controller adds no
demonstrated benefit. Python runtime reuse is rejected. Two binaries require
explicit compatibility and integration testing; gRPC is unnecessary initially.

## Compatibility and validation

All public formats start at v1. No LexAI history is imported. Build the Go fake
kernel first; Rust RI and cross-process qualification follow. No RI functionality
is implied by accepting this decision.

## References

- [LexAI audit](../research/lexai-mechanism-audit.md)
- [Runtime contract](../specifications/runtime-protocol.md)
