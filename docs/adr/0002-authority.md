# 0002: Models reason; the controller authorizes

Status: ACCEPTED
Date: 2026-09-06

## Context and problem

Repository text and model output are untrusted inputs. A useful planning result
does not grant permission to mutate a repository or perform external effects.

## Decision and rationale

Models own interpretation, relevance and implementation judgment. The controller
owns exact identity, permissions, transitions, durable effects and verification.
Plans require an explicit approval bound to their exact content and run identity.
One primary writer owns a change-set. Explorers are advisory and read-only; no
cheaper model may prevent a stronger model from reading source.

## Alternatives and consequences

Implicit approval from prose and model-selected verification are rejected:
neither establishes authority. A rigid deterministic task planner is also
rejected because it would replace model judgment. Explicit bindings add friction
when inputs change, but prevent stale approvals from authorizing different work.

## Compatibility and validation

No private role catalogue is retained. Tests must reject stale approval, invalid
transitions and model substitution. Worktrees are not OS security sandboxes.

## References

- [State machine](../specifications/state-machine.md)
- [Repository identity](../specifications/repository-identity.md)
