# Run state machine v1

Scope: one run and one change-set. The first event MUST be `run.created`, binding
repository identity, objective and runtime profile. Its state is OBJECTIVE.
`planning.started` enters PLANNING. `plan.recorded` binds a validated invocation
result and enters AWAITING_APPROVAL. `plan.approved` names the exact plan digest
and an explicit human actor; it enters IMPLEMENTING. Approval for another plan
MUST fail. Repository/model prose never supplies approval implicitly.

Later implementation phases add guarded transitions through VERIFYING, REPAIRING,
optional REVIEWING, READY and HANDED_OFF. These states are reserved, not reachable
through arbitrary transition events in the initial kernel. READY requires all
required checks passing against the exact candidate. An unresolved effect MUST
block further effect execution until explicit reconciliation.

Replay MUST validate every payload and transition in order; an unknown event or
out-of-order event fails. Run ID is a domain-separated hash of immutable creation
inputs, including a caller-generated nonce to distinguish repeated objectives.
Plan ID is a domain-separated hash of the admitted result. No wall-clock value
determines permission. Replaying the same bytes returns the same state.

Concurrent controller writers are serialized by journal locking; appending an
event must validate the complete prior state under that same lock. A controller
cannot rely on a stale read before acquiring the lock. On persistence failure,
the caller treats outcome as uncertain and inspects durable state.

Initial example: create → start planning → record fake-runtime plan → explicit
approval. The fake result proves orchestration mechanics, not model quality.

In IMPLEMENTING, `workspace.intent` binds the exact run/source/destination request
and sets workspace outcome UNKNOWN. A second intent is forbidden. Only an observed
`workspace.confirmed` receipt bound to that request and a pristine candidate sets
CONFIRMED. An unconfirmed intent cannot be retried by `run`; `reconcile` performs
fresh observation and confirms only exact pristine files and index. Absent,
partial, unregistered or modified workspaces remain UNKNOWN without mutation.

After workspace confirmation, `files.intent` binds the current admitted candidate,
validated whole-tree prediction and explicit effect authorization. Its outcome is
UNKNOWN until `files.observed` binds an actual observation. CONFIRMED advances the
admitted candidate to the prediction; NOT_APPLIED retains the before candidate;
UNKNOWN blocks another proposal. Reconciliation can append fresh observations
only while UNKNOWN. Intent IDs cannot be reused, even after NOT_APPLIED; a new
attempt requires a fresh proposal nonce and explicit approval.
