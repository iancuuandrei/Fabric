# File effects v1

Scope: explicitly approved regular-file creates, replacements and deletes inside
one admitted worktree. A proposal binds a nonce, full before candidate, before
file manifest, exact ordered changes and predicted after candidate. Each change
names a portable writable path, expected before SHA-256 or null for absence,
canonical base64 content or null for deletion, and executable mode. Empty content
is distinct from deletion. Windows executable-bit changes are unsupported.

The manifest MUST hash to the before candidate. Prediction MUST preserve every
unmentioned file, HEAD and index; it MUST reject case aliases, file/directory
collisions, mismatched before hashes, protected paths and no-op proposals.
Limits: 64 changes, 256 KiB decoded content in total, existing candidate limits,
and the journal's 1 MiB event bound. Oversized proposals fail before execution.

The controller holds the writer lease, checks the current candidate, validates
explicit approval of the exact intent ID, and persists `files.intent` before
mutation. The adapter uses rooted operations, rechecks each before state and
writes a temporary regular file followed by rename. It never writes through an
existing target handle or deletes recursively. Missing parent directories may
be created for admitted new paths. Empty directory metadata is not tracked by
Git or the file candidate; NOT_APPLIED describes file state, not directory history.

After success or failure the controller observes the whole candidate. Exact after
state yields CONFIRMED; exact before state yields NOT_APPLIED; mixed state,
unexpected files, missing observation or leftover temporary files yield UNKNOWN.
Receipts bind the observation hash to the exact intent. A missing receipt is
UNKNOWN. Further effects are blocked while UNKNOWN. Reconciliation observes
actual state; it never retries a write automatically.

Multiple file changes are ordered, not transactionally atomic. A process failure
may leave a partial change-set. Unknown state requires explicit reconciliation;
manual recovery must not be disguised as automatic rollback. Receipt construction
alone does not establish observation provenance. Source byte and permission checks
are not an OS sandbox against another process with equal filesystem privileges.
