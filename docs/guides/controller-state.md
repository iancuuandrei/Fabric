# External controller state

Repository-owned configuration is separate from mutable controller state. Set
an explicit absolute directory in `harness.toml` for new externally stored runs:

```toml
controller_state_root = 'D:\EngOrch-control'
```

The selected directory must be disjoint from the repository source and Git
control paths. The controller derives a namespace under `repositories/` from the
stable checkout identity: repository name, source root, Git common directory,
and object format. HEAD and tree hashes remain properties of individual runs;
advancing HEAD does not change the state lookup namespace. An identity marker
prevents accidental reuse of a namespace for another checkout.

Run journals, journal-derived sidecars, scheduler state, writer tokens and lease
guards use that namespace. Configuration remains in the repository. Runtime
homes remain separately selected by the existing Codex/OpenCode configuration.
The Git workspace destination retains its existing bounded native worktree
layout; mutable controller journals and lease files are outside the agent's
target worktree. A worktree is change isolation, not an OS sandbox.

The controller binds its journal path and workspace lease namespace to the
immutable run configuration. It rejects substituted paths, unsafe overlap and
filesystem aliases. Inspecting a run does not silently initialize or relocate
state. A partial or mismatching identity marker is an error, not permission to
overwrite it or guess which repository owns the state.

Omitting `controller_state_root` preserves historical repository-local state
and receipt identities. This is compatibility for existing histories; it does
not migrate them. New qualification runs explicitly select an external root.

## OpenCode snapshot metadata

Semantic assistant text and OpenCode runtime metadata are different evidence.
A snapshot `patch` is not automatically a model-generated source edit. The
runtime validates and retains patch identity, paths and classification separately
from the text consumed by the worker contract.

For read-only and proposal-only execution, an empty patch can accompany a valid
result. A patch naming repository source is an authority-violation signal and
blocks completion. A patch naming only exact known controller journal paths is
state-contamination evidence and also blocks completion. Unknown or contradictory
metadata does not become successful output. Neither classification grants retry,
fallback, file mutation or access settlement authority.

External state removes controller journal churn from the source snapshot. R11
qualification leaves OpenCode snapshots enabled to verify this separation;
disabling snapshots is not used to hide contamination.
