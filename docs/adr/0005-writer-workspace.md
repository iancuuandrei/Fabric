# 0005: One writer in a bound Git worktree

Status: ACCEPTED
Date: 2026-09-06

## Context and problem

Implementation requires source mutation while the user's checkout may be dirty.
Worktree paths can be substituted, files can be linked outside the intended root,
and Git checkout hooks/filters can run commands before admission.

## Decision and rationale

Create a deterministic per-run Git worktree from the approved immutable commit.
Use no-checkout registration and materialize regular Git blobs directly, followed
by index initialization. Disable Git hooks and fsmonitor. Do not run checkout
filters. Reject symlinks, submodules and special files in the initial writer
workspace; this restriction must be reported explicitly.

The controller holds one exclusive writer lease across validation, effects and
observation. A crashed lease is never stolen automatically. File access uses
Go's rooted filesystem API plus explicit path, link/reparse and hardlink checks.
Updates replace file entries using temporary files rather than writing through
existing hardlinks. Candidate identity includes file bytes and executable modes.

## Alternatives and consequences

Changing the canonical checkout is rejected. Normal checkout is convenient but
can invoke repository-configured hooks and filters. A worktree is not an OS
sandbox and cannot isolate arbitrary verification commands or a hostile process
with the same filesystem authority. Containers are deferred to WorkerEnvironment.

Scanning candidate files is deliberately bounded and measurable. It may be more
expensive than Git's stat cache, but avoids treating clean-filter output as
unfiltered source evidence. RI indexing remains a separate Rust responsibility.

## Compatibility and validation

This is v1 workspace admission. Tests must use real unrelated temporary Git
repositories, preserve dirty canonical files, reject stale commits/worktrees,
detect link aliases and show lease exclusion. Failed creation requires explicit
observation/reconciliation before another attempt.

## References

- [Workspace specification](../specifications/worktree.md)
- [Effect model](../specifications/effect-model.md)
