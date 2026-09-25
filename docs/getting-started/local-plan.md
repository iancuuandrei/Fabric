# Run and inspect a local fixture plan

Build `cmd/harness` using the root README. Put the resulting executable on PATH
or invoke its absolute path. The target must have a Git commit; an unborn
repository deliberately fails identity discovery. No network or model service
is used by the fake runtime.

Run `harness --root PATH init`, then edit `PATH/harness.toml`. `init` creates
a fake config for local fixture runs; a real OpenCode runtime requires a
separately configured and qualified runtime. The default check is Go tests;
use checks appropriate to your repository. Verification checks are executed
by the harness when the run reaches the verify stage.

Run `harness --root PATH doctor` to validate local configuration and commit
identity. `doctor` never proves provider execution.

Governed stages are distinct: `plan`, exact `approve`, `run`, a `write`
proposal, `prepare-files` / `apply-files`, `verify`, independent `review`,
and `commit`. `run` does not execute the later stages automatically.

Run `harness --root PATH plan "Your objective"` to produce a fixture plan. The
JSON result includes `run_id`, `plan_id`, the exact configuration, repository
commit/tree, requested model and observed fake identity. Usage is null.

Inspect the recorded plan with `harness --root PATH inspect RUN`. Explicitly
approve it with `harness --root PATH approve RUN PLAN ACTOR`, supplying the exact
IDs and your operator label. This records approval and enters IMPLEMENTING.
No source file is changed by approval. `harness --root PATH run RUN` then creates
the bound isolated workspace and records its candidate identity. The original
checkout remains untouched; continue with [exact file proposals](../guides/file-changes.md).
An actor label records operator input and may name delegated automation; it is
not necessarily a human decision or cryptographic authentication.

Use `harness --root PATH resume RUN` after interruption. Completed planning is
replayed without rerunning the fake. A interrupted fake planning invocation can
be repeated because it has no effects; this does not establish retry semantics
for real providers. Use `status` to validate and list local run histories.
An UNKNOWN external effect must not be blindly resent; `status`, `inspect`,
and `reconcile` are readback paths for reconciling it.

State lives under `.harness/runs/` by default, when no `controller_state_root`
is configured. Ignore `.harness/` in the target repository.
The harness does not automatically edit its ignore file. Do not edit journal
bytes: partial writes and hash mismatches fail replay. A leftover `.lock` means
an interrupted owner may need review; automatic stale-lock removal is absent.
Never remove a lock without establishing that its writer is stopped and checking
the journal. The initial kernel assumes operator-controlled local state paths.

Workspace creation records intent before touching Git. If confirmation is lost,
`run` refuses to repeat creation. Use `harness --root PATH reconcile RUN` to inspect
actual registration, raw source files and index. It confirms only a pristine
workspace; absent, partial or modified state stays UNKNOWN. It does not remove
stale writer locks. A worktree is change isolation, not an OS security sandbox.
