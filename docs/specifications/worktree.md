# Writer workspace v1

Scope: controller-owned change-set isolation, not OS security isolation. A binding
contains run ID, source repository identity, exact base commit, absolute worktree
path, deterministic branch and actual per-worktree Git directory. The destination
MUST be `.harness/worktrees/RUN` beneath the source checkout. Branch is
`harness/RUN`. Existing unrelated destinations/branches MUST NOT be reused.

Creation MUST be preceded by durable controller intent. Registration uses Git
`worktree add --no-checkout`; regular blobs are materialized directly without
checkout hooks or filters. Symlinks, submodules and unsupported modes are rejected.
The index is initialized from the approved commit. A caller MUST record actual
post-state. An interrupted creation is UNKNOWN until explicit reconciliation;
the worktree package does not own the run journal or automatic retry.

Admission checks path, common Git directory, worktree-specific Git directory,
HEAD, branch and repository identity. One exclusive lease per run/change-set
serializes writer operations. Lease cleanup requires the same owner; a crashed
lease MUST NOT be stolen automatically. Source checkout dirty files are preserved.

Read-only agent work uses `AcquireRead(Request)`. Readers take a shared kernel
lock on the stable per-run `.guard` file; writers and explicitly authorized lease
recovery take the exclusive form of the same lock. Multiple processes may hold
the shared guard concurrently. A writer cannot create an intent or mutate while
any reader holds it, and a reader cannot start while a writer owns it.

The existing `.lock` file remains the durable writer token. It is created only
by writer acquisition, verified before removal by that owner, and retained after
a crash. A reader takes its shared guard before checking for this token, so an
orphaned token fails closed even after the crashed process's kernel locks have
been released. Only the existing explicit quiescence, token-bound authorization
and adoption flow may recover it. The `.guard` file is an empty, singly linked,
persistent coordination object; reader close releases its handle and never
deletes either coordination file.

`ReadLease.WithOwnership` binds the exact immutable `Request` while a callback
runs and serializes that callback with close. Its `LeaseIdentity` attests only
that the shared exclusion remains held. `ReadLease` is a distinct type, carries
no writer token, and is not accepted by worktree mutation APIs. Controllers must
fingerprint the exact candidate before and after a read-only runtime; the guard
does not establish candidate freshness or authorize any file effect.

Acquisition is non-blocking. Callers may schedule concurrent readers, but must
surface writer/read contention instead of silently weakening exclusion. Process
exit releases kernel guards. Tests execute multi-process sharing, exclusion,
crash release, stale-token rejection and ownership/close checks on Windows; the
same package is cross-compiled for Linux and macOS lock implementations.

Candidate fingerprinting observes every regular file beneath the workspace except
its `.git` metadata entry. It includes relative path, SHA-256 bytes and executable
mode plus HEAD and index content. Symlink/reparse/hardlink and special-file inputs
fail. Limits: 4096 files, 64 MiB per file and 512 MiB aggregate content. Bound
failures are explicit, not partial successful candidates. A second fingerprint
must bracket operations when freshness is required. Git's ignored-file status
does not hide source input from this fingerprint.

Mutation paths use relative forward-slash names; traversal, absolute paths,
backslashes, Windows device aliases, trailing dot/space and control-path components
are forbidden. `.git`, `.harness`, `.codex`, `.agents`, `AGENTS.md`, `harness.toml`
and workflow configuration cannot be modified by a writer proposal. Rooted file
operations enforce containment, while link and hardlink checks reject aliases.

The host and state directory remain operator-controlled. No claim is made against
a hostile same-user process replacing metadata or opening unrelated paths through
an arbitrary child command. WorkerEnvironment qualification is separate.
