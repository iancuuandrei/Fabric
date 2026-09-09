# Local commit preparation

`PrepareCommit` captures a READY candidate under the workspace lease and returns
a `CommitPlan` plus a commit-kind effect identity. It replays state after acquiring
the lease and rejects filesystem drift from the verified/reviewed candidate.
Preparation does not stage files, write Git objects, move refs or publish anything.

The plan binds the complete file manifest, workspace/branch and single parent,
explicit author and committer identities, their Unix timestamps, exact UTF-8
message and an attempt nonce. Attribution never comes from ambient Git user
configuration. Current metadata uses UTC; the message requires LF line endings
and a final newline, with no silent normalization. Git header delimiters/control
characters in attribution are rejected. The manifest must reconstruct the
candidate's exact file identity and count.

This follows Git's separation of trees, commit objects and ref updates; consult
[git-commit-tree](https://git-scm.com/docs/git-commit-tree) for the underlying commit
metadata and date semantics (consulted 2026-09-07). The eventual executor must bind
object creation and compare-and-swap ref update to explicit authorization and
readback. That executor and uncertainty reconciliation are not implemented by this
preparation API. Push and draft-PR effects remain separate and unperformed.

Executed fixture checks cover pre-verification rejection, READY capture without
journal modification, message/time identity changes, header-injection and manifest
substitution rejection, and candidate drift rejection.

`ObjectID` implements Git's type/size header and native SHA-1/SHA-256 digest.
`CommitObject` encodes the exact single-parent commit body without writing it.
It validates object-ID format but does not prove that the supplied tree represents
the manifest or exists in storage. Tests compare native object hashes with Git
and compare complete commit bytes and IDs against `git commit-tree` in disposable
repositories. Those fixture commands write unreachable commit objects; they do
not move refs. Controller commit execution and recovery remain incomplete; the
lower-level storage and ref adapters are described below.

`TreeObject` encodes immediate children with Git's directory-aware ordering and
binary object references. It admits regular files, executable files and trees;
it rejects symlinks, submodules, ambiguous portable names, duplicate names and
case collisions. Input order is preserved in the caller's slice. Executed SHA-1
and SHA-256 comparisons against `git mktree` and `git cat-file` cover empty trees,
UTF-8 names, executable modes and the `foo.bar`, directory `foo`, `foo0` sorting
boundary. These tests deliberately use `mktree --missing`: child existence is not
proven by this encoder.

`BuildTrees` constructs recursive directory objects from portable file paths,
native blob IDs and executable flags. It returns a deterministic postorder with
the root last, rejects duplicate paths, file/directory conflicts and case aliases
across directory components, and handles an empty manifest. Executed comparisons
with Git's index and `write-tree --missing-ok` verify every emitted directory's
bytes and the root ID for SHA-1/SHA-256, including reversed input order. Binding
the supplied blob IDs to freshly read, admitted candidate bytes is still required
before the future executor may use these objects.

`ReadCandidateObjects` now performs that binding under a caller-held workspace
lease: whole-candidate fingerprints bracket regular-file reads; each read must
match the manifest's SHA-256 and executable flag. Native blob IDs are computed
from those bytes, then recursive trees and the commit are encoded. Retained blob
content is bounded to 64 MiB across the candidate. Errors return no usable object
set. Executed pristine-candidate fixtures compare blob bytes, root trees and commit
bytes with Git for both object formats, and reject later candidate drift. This
does not freeze the filesystem against external writers or persist any objects.

`CandidateObjects.Validate(plan)` independently reconstructs the object set from
the supplied blob bytes and the approved manifest. It checks content SHA-256,
native blob IDs, exact paths/order/count, every recursive tree's bytes and ID,
and the exact commit body and ID. This is the validation boundary for mutable
in-memory object sets before a storage adapter can accept them. Executed tests
reject substitutions of blob paths, bytes and IDs; missing blobs/trees; altered
tree paths, bytes and IDs; and altered commit bytes/IDs for both Git formats.
Validation alone grants no execution authority and produces no storage receipt.

`StoreObjects` validates the commit intent and exact authorization, validates the
object set, then brackets storage with candidate fingerprints. Git writes blobs,
trees and the commit and each written object is read back byte-for-byte. Calls
disable replacement objects, hooks, ambient Git environment/configuration and
lazy fetching; subprocess output and elapsed time are bounded. Duplicate object
IDs are written once. The caller must hold the lease and persist intent first;
controller integration is still pending. Errors may leave unreachable objects and
must not be classified as NOT_APPLIED or retried automatically. Executed temporary
repository fixtures cover SHA-1/SHA-256 storage/readback and invalid authorization
or intent rejection. No refs or index are updated by this adapter.

`AdvanceRef` validates authorization and object bytes, checks the candidate and
stored commit, then uses `update-ref --no-deref` with the exact old and new IDs.
Git atomically checks the old branch ID; HEAD's symbolic target and candidate
files/index are checked before and after the operation, not in the same atomic
transaction. A combined HEAD verification/branch update was rejected by Git
2.53.0 as overlapping updates, so that stronger claim is not made. See
[git-update-ref](https://git-scm.com/docs/git-update-ref), consulted 2026-09-07.
Post-update errors require reconciliation and can occur after the branch moved.
The adapter leaves the index unchanged. Executed SHA-1/SHA-256 fixtures verify
authorized advancement, approval rejection, rejection of reuse with a stale
parent, readback, and unchanged canonical source HEAD. Tests restore only their
temporary fixture ref before independently checking file drift. Durable controller
integration, index finalization and recovery are still required.

`RecordCommitIntent` begins controller integration. It validates the exact READY
workspace/candidate, run/plan/repository effect binding and operator authorization,
then rereads under lease and derives the predicted tree/commit IDs from candidate
bytes. `commit.intent` stores these predictions and transitions replay to
COMMITTING with UNKNOWN outcome. It writes no Git objects. Replay rejects a
second intent or unrelated subsequent events while the outcome is UNKNOWN;
there is currently no controller path that resumes or executes this intent.
Executed fixtures verify durable replay, missing-approval rejection without
journal modification, absence of the predicted commit object, and blocking of
duplicate intents and unrelated events. The object predictions are not storage
evidence; the eventual executor must rederive and compare them before mutation.

`FinalizeIndex` completes the lower-level sequence after ref advancement. It
checks authorization, objects, post-commit workspace binding, unchanged file
manifest and the original index fingerprint, then uses `read-tree` without `-u`
to replace only the isolated index. It verifies the index against the commit with
`diff-index --cached` and checks candidate files again. It returns the observed
post-commit candidate; errors can follow index mutation and do not authorize retry.
Executed SHA-1/SHA-256 fixtures add a binary file through governed file effects,
verify it, record commit intent, store objects, advance the ref and finalize the
index. Git reports a clean worktree, the file bytes remain exact, and the canonical
checkout has no added file. Premature finalization, missing approval and reuse
after an index change are rejected. Controller orchestration and receipt/recovery
admission and execution are described below.

`ObserveFinalized` is a read-only adapter that checks the post-commit binding,
reconstructs the trees from admitted file bytes, reads every blob/tree and the
exact commit back from Git, verifies the index and brackets observation with
candidate fingerprints. `ReconcileCommit` runs it under lease and records a
hashed `commit.observed` receipt. Exact final state transitions the controller to
COMMITTED/CONFIRMED with the new binding/candidate; failed observation retains
COMMITTING/UNKNOWN and never claims NOT_APPLIED or retries writes. Executed
SHA-1/SHA-256 fixtures reject an unfinished index, retain UNKNOWN before execution,
confirm the complete object/ref/index sequence by read-only reconciliation and
reject a duplicate observation after confirmation. Process quiescence after a
controller crash and explicit recovery of partial states still need qualification.

`ExecuteCommit` now orchestrates a new authorized attempt under one continuous
workspace lease. It revalidates READY state, derives objects, appends intent before
mutation, writes/readbacks objects, advances the ref, finalizes the index and
records final observation. Later steps do not execute after a prior step fails.
Observation runs with a separate bounded context even if execution was cancelled;
errors are returned along with the latest journal state. An existing intent is
never resumed through this entry point. Executed controller fixtures for both Git
formats include an added binary file, CONFIRMED replay, clean worktree, unchanged
canonical checkout, missing-authorization rejection and duplicate-execution
rejection. CLI exposure and process-interruption/recovery qualification remain
outstanding at that stage; CLI exposure is now described below. No EngOrch
checkout commit or publication was performed.

## CLI

`prepare-commit RUN METADATA_JSON` reads strict JSON containing `author`,
`committer` and `message`. Each identity contains `name`, `email` and
`unix_seconds`; the message must end in LF. No ambient identity or clock is
substituted. Save the canonical JSON preview returned by this command, inspect
its plan and `intent_id`, then execute `commit RUN PREVIEW_JSON INTENT_ID ACTOR`
with that exact approval. The commit command emits the latest controller snapshot
even when execution returns an error. `reconcile RUN` selects pending commit
observation before other effect families and performs no retry.

The executed CLI fixture covers plan approval, workspace creation, a governed
file addition, verification, commit preparation, wrong-approval rejection,
successful local commit and rejection of repeated execution. Generated command
reference consistency is checked. Abrupt-process recovery qualification remains
incomplete; a successful fixture is not a crash-recovery guarantee.

## Process interruption evidence

Executed SHA-1 fixtures launch a separate Go test process and exit it without
deferred cleanup at four boundaries: recorded intent, stored objects, advanced
ref and finalized index. The parent waits for the specific exit status, then
checks actual HEAD, journal state and the abandoned lease. All four preserve
COMMITTING/UNKNOWN; normal reconciliation refuses the existing lease and leaves
the journal unchanged. This is process-exit evidence at completed subprocess
boundaries, not evidence for termination during an in-flight Git subprocess or
power loss. The fixtures do not reclaim the lease. Explicitly authorized lease
recovery with quiescence evidence is still required before crash recovery can
complete; no automatic stale-lock deletion is implemented.

Workspace leases now also hold nonblocking OS exclusion for the handle lifetime:
Windows uses `LockFileEx`, while supported Unix builds use `flock`. The persistent
lock file still prevents ordinary acquisition after a crash; kernel exclusion is
a prerequisite for explicit recovery, not permission to reclaim automatically.
Executed Windows tests verify competing-handle rejection, token readability and
release after handle close, alongside the full worktree test package. Unix runtime
behavior has not been executed in this Windows qualification. The Windows byte
range is placed at 4 GiB so bounded token reads do not overlap it. The API permits
locks beyond EOF; see [LockFileEx](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-lockfileex), consulted 2026-09-07.

`PrepareLeaseRecovery` now binds the observed token SHA-256, request, nonce and
explicit stopped-workloads evidence. `AdoptLease` requires an exact
`lease_recovery` effect authorization and a caller-persisted intent; it obtains
kernel exclusion on the original file and rechecks token and file identity. It
returns ownership without replacing or deleting the file; normal lease close
releases it. Executed Windows tests reject an active owner, missing approval,
changed on-disk token, substituted plan, a competing recovery owner and reuse
after release. This test simulates handle closure while retaining the file; it
is separate from the process-exit tests above. Controller recovery journaling
and CLI exposure are still pending. Operator evidence remains an attestation;
kernel exclusion alone does not establish orphan-process quiescence.

Controller recovery is now available through `PrepareCommitLeaseRecovery` and
`RecoverCommitLease`. The controller records `commit.lease-intent` with distinct
effect authorization before adoption, observes commit state while holding the
adopted lease, then closes the lease and records `commit.lease-observed` with a
hashed release observation. Recovery uncertainty blocks unrelated events. A
failed/interrupted recovery cannot be silently retried through the same API.
Executed process-exit fixtures now exercise this path: each abandoned lease is
released under fresh approval; complete commit state becomes CONFIRMED, while
pre-ref partial state remains UNKNOWN. These fixtures commit an unchanged tree,
so an index already matches immediately after ref advancement. No interrupted
write is resumed. CLI exposure and recovery of partial commit/recovery effects
are still outstanding.

The CLI now exposes `prepare-commit-lease RUN EVIDENCE workloads-stopped` and
`recover-commit-lease RUN PREVIEW_JSON INTENT_ID ACTOR`. The preview binds the
operator's evidence and observed token; execution requires its exact approval.
Recovery emits the latest snapshot even when commit observation fails. Thus a
released lease can be CONFIRMED while the commit remains UNKNOWN and the command
returns an error. Executed CLI fixtures cover this distinction, missing stopped
attestation, wrong approval and actual lock removal. This CLI fixture uses an
explicitly ownerless token; actual process-exit coverage remains in the controller
tests. Completion of partial commit effects still requires a separate recovery
design and authorization path.

`InspectRecovery` now observes the exact states from which that future recovery
path may proceed. BEFORE_REF requires the original candidate and reproduces the
predicted tree/commit IDs, regardless of whether unreachable objects have already
been stored. AFTER_REF requires the expected commit, verified object readback and
candidate files with the original index. FINALIZED additionally requires an index
matching the commit. Foreign HEADs, file drift and unrecognized partial indexes
are rejected. This API only observes; it grants no retry authority and does not
assert process quiescence. Qualification adds these states to the existing binary
file fixtures for both Git object formats.

`RecoveryPlan` defines a distinct `commit_recovery` approval target. It binds the
full original commit plan and effect, exact target tree/commit IDs, observed
partial candidate and stage, fresh nonce, and explicit stopped-workload evidence.
BEFORE_REF must match the original candidate; AFTER_REF must match its post-commit
binding while retaining the original index. FINALIZED is rejected because it
requires observation rather than mutation. Tests exercise original-approval
rejection, nonce/evidence identity changes, missing quiescence and substituted
index/original-effect bindings across the existing SHA-1/SHA-256 stage fixtures.
The contract grants no authority until its new effect is explicitly approved;
its execution and controller recovery journal transitions are still pending.

`Recover` now provides the lower-level executor for that distinct authorization.
It reobserves the partial state and requires exact equality with the preview.
BEFORE_REF rebuilds/verifies candidate objects, writes them, advances the ref and
finalizes the index; AFTER_REF reconstructs/verifies the same objects and only
finalizes the index. Both paths finish with full read-only commit observation.
The caller must hold the lease and persist the new recovery intent first. Any
error can follow mutation and never grants retry authority. Recovery controller
journal transitions and CLI commands remain pending. Fixtures cover both paths
with a binary file addition and both native Git object formats, including missing
approval and stale-preview rejection, clean worktrees and canonical preservation.

`PrepareCommitRecovery` and `RecoverCommit` now integrate the new approval into
controller replay. Preparation captures the partial state under lease; execution
rechecks it, appends `commit.recovery-intent` before mutation, invokes the recovery
adapter and records final observation even after an execution error. Replay binds
both the original commit receipt and the recovery effect receipt to that final
observation hash. Recovery intent alone remains UNKNOWN and cannot be reused.
Process-exit fixtures exercise controller completion after intent-only and
object-storage interruptions, including separately authorized lease recovery,
rejection of the original commit approval and rejection of repeated recovery.
These controller crash fixtures use SHA-1 and an unchanged tree; changed-tree
BEFORE_REF/AFTER_REF adapter fixtures cover both Git formats. Recovery CLI exposure
and repeated interruption during recovery remain outstanding.

The CLI now exposes `prepare-commit-recovery RUN EVIDENCE workloads-stopped`
and `recover-commit RUN PREVIEW_JSON INTENT_ID ACTOR`. The preview is a new
approval target; the original commit approval is rejected. Executed CLI fixtures
continue from authorized lease release with commit UNKNOWN to separately approved
commit recovery and COMMITTED/CONFIRMED, rejecting missing quiescence, original
approval reuse and repeated recovery. The generated CLI reference matches these
commands. Repeated interruption during recovery remains unqualified.

Lease recovery now supports explicitly approved successor attempts through
`previous_intent_id`, which is hashed with the token and evidence. Replay requires
the exact latest predecessor, so an interrupted approval cannot be reused. This
also permits release recovery when the commit is already CONFIRMED but lease
release remains UNKNOWN; the commit observation is not repeated in that case.
Executed Windows/SHA-1 process fixtures exit successively after lease-recovery
intent and after commit observation while retaining the lease. A newly approved
successor releases it, preserves the confirmed commit and rejects both stale
approvals. In-flight Git termination, power loss and repeated partial-commit
recovery interruptions remain outside this executed evidence.

Partial-commit recovery now also binds `previous_intent_id`. A successor must
reference the latest recovery attempt and capture current state again under
lease; its nonce, evidence and approval are new. The prior attempt remains in
journal history and cannot be executed again. Executed Windows/SHA-1 fixtures
exit a process immediately after commit-recovery intent while retaining its
lease, recover that lease under separate approval, reject reuse of the interrupted
commit-recovery approval, then complete the newly approved successor. This proves
that specific repeated-interruption path; termination during recovery Git writes
and power loss are still unqualified.
