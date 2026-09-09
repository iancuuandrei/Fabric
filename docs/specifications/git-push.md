# Governed Git push

The initial push contract binds the original repository identity, committed
workspace/candidate, explicit destination, target branch, expected old remote
commit and nonce. Its effect envelope binds the run and approved implementation
plan. Changing destination, branch, expected state or nonce invalidates approval.
Validation is pure: it establishes neither local cleanliness nor remote state.

`expected_old: null` means the branch must be absent. An empty or all-zero object
ID is rejected rather than treated as another representation of absence. Existing
commits must match the repository's SHA-1 or SHA-256 object format. Targets are
full `refs/heads/...` names; deletion, tag publication and implicit ref selection
are outside this contract.

Destinations are explicit HTTPS URLs without embedded credentials, query strings
or fragments, or absolute local paths for bare-repository qualification. Ambient
remote aliases, SSH and UNC paths are not admitted by this version. Transport and
credential handling remain to be implemented; accepting a plan is not a network
operation or permission to publish this project.

The executor uses an explicit expected-old ref comparison and preserves
non-fast-forward protection through a separate ancestry check, and independently
observes the remote ref. Outcome classification belongs to the controller. Git documents the exact
`--force-with-lease=<refname>:<expect>` form and empty expectation for an absent
ref in its [push reference](https://git-scm.com/docs/git-push). An explicit lease
alone is not a non-fast-forward policy and does not establish outcome after a
lost response. Controller intent, execution, observation and reconciliation are
still required. No automatic retry follows an uncertain execution.

Executed evidence currently covers payload identity, changed-input approval
rejection, object formats, candidate/workspace consistency, and rejected ambiguous
destinations/ref names. The controller and CLI lifecycle are implemented locally; process interruption and recovery qualification remain incomplete.

`ParseAdvertisement` now validates successful `ls-remote --refs` output for the
exact requested branch. Empty successful output preserves absence; duplicate,
partial, foreign-ref and malformed object responses fail. The parser does not
establish transport success or destination identity. Tests cover both object
formats. Controller admission and read-only reconciliation are implemented; process-kill recovery remains pending.

`ObserveRemote` now runs the read-only query through Git with a 30-second bound
and bounded stdout/stderr. A temporary working directory and Git discovery ceiling
prevent repository configuration from selecting another destination. Inherited
Git variables, global/system configuration, credential helpers and HTTP redirects
are disabled; no objects are fetched. Authentication support is not implemented.
Process failure returns no observation, even when stdout is empty. Cleanup failure
also invalidates the observation. Raw stderr is not returned as public evidence.

Real local bare-repository tests pass for SHA-1 and SHA-256: absent and existing
branches, an unrelated branch, ambient URL/Git-directory substitution, cancellation
and a nonexistent destination. The bounded capture also passes an `io.Copy`
regression. These tests do not qualify HTTPS, authentication or push execution.

`Execute` now validates the exact effect envelope and authorization, requires a
caller-held lease and persisted intent, and compares the pristine committed
candidate with the plan. It observes the expected old ref before mutation. An
isolated temporary bare repository reads the source object database without
using source repository configuration. A separate merge-base check rejects
unknown or divergent ancestors. Push specifies only the exact commit/ref pair,
expected-old lease, no followed tags and no recursive submodule publication.
Readback and source freshness checks have independent bounded contexts after the
push attempt, including when its command fails. Errors can follow remote mutation
and never authorize retry. Temporary cleanup verifies its resolved parent first.

PASS: actual local bare SHA-1/SHA-256 create and fast-forward pushes, rejected
divergent histories, missing approval and reused stale expectation, and unchanged
canonical branch/candidate. Fixture intent files were synced before execution.
This lower-level executor evidence does not establish
concurrent remote-race coverage, process-kill recovery, authenticated HTTPS or
publication of EngOrch. Those boundaries remain unfinished.

## Controller and CLI

PreparePush requires COMMITTED with confirmed commit/workspace evidence and no
prior push. Under lease it verifies the committed candidate and observes the
remote expectation. ExecutePush revalidates exact payload and authorization,
persists push.intent, enters PUSHING/UNKNOWN, invokes the executor once and
records independent push.observed readback. Only matching remote commit plus the
unchanged committed candidate produces CONFIRMED/PUSHED. This state does not
mean a draft PR exists or the run is HANDED_OFF.

While push is UNKNOWN, replay permits only observation events. ReconcilePush
reads state under lease and never repeats the push. An absent branch remains
UNKNOWN, since present absence cannot establish that a previous effect never
occurred. Foreign branch observations and NOT_APPLIED claims derived from absence
are rejected. Existing push intents cannot be passed to ExecutePush again.

CLI commands are prepare-push RUN DESTINATION TARGET_REF and push RUN
PREVIEW_JSON INTENT_ID ACTOR. The ordinary reconcile command selects pending
push reconciliation. Preview approval and run/repository bindings are exact;
execution errors preserve the latest snapshot output.

PASS local controller SHA-1/SHA-256 execution and controlled receipt omission,
read-only confirmation afterward, absent-branch uncertainty and repeated-effect
rejection (44.86s). Additional forged-outcome/foreign-ref tests pass. The CLI
fixture completes prepare/approve/push with wrong approval and repetition
rejection. These are local bare-repository tests, not actual process-kill,
retained-lease recovery, authenticated HTTPS or external publication evidence.

A changed-remote fixture also passes: another local actor creates the branch
between preview and execution. The controller records UNKNOWN, preserves that
commit and refuses retry. This exercises preflight change, not a race inside
receive-pack or process termination.

## Interrupted push lease

Push lease recovery now requires a separate preview/authorization with the exact
retained token digest and explicit workload quiescence evidence. Its repository
binding is the post-commit workspace source, not the run's earlier source commit.
Recovery persists push.lease-intent before kernel lease adoption, observes remote
state without another push, releases the owned lease and records release evidence
separately from push outcome. UNKNOWN lease recovery blocks other effects; fresh
successors bind the preceding recovery intent. No automatic retry is introduced.

The CLI exposes prepare-push-lease RUN EVIDENCE workloads-stopped and
recover-push-lease RUN PREVIEW_JSON INTENT_ID ACTOR. A release may be confirmed
while the original push remains UNKNOWN.

PASS Windows/SHA-1 child-process exits immediately after durable intent and after
the local Git push completed: retained lease blocks ordinary reconciliation,
original push approval cannot authorize recovery, separately approved recovery
releases the lease, and only the completed effect is confirmed. The pre-effect
case remains UNKNOWN. These controlled exits do not qualify termination during
transfer, power loss, SHA-256 process exits or interrupted recovery successors.
The first run rejected a mismatched original-source identity; using the exact
post-commit workspace identity corrected that contract mismatch.

Successor recovery qualification now also passes on Windows/SHA-1: a worker exits
after push.lease-intent, then another exits after remote confirmation but before
lease release. The next separately approved recovery binds the latest predecessor,
rejects earlier approvals and releases the lease without repeating the push or
its already-recorded confirmation. This is controlled process-exit evidence,
not transfer-time termination or power-loss qualification.

Explicit HTTPS credential foundation: NewCredential binds an in-memory GitHub
token to one exact public-GitHub repository destination. ObserveRemoteAuthenticated
passes a URL-scoped Basic authorization header through child-only GIT_CONFIG
runtime environment pairs, following the [Git config contract](https://git-scm.com/docs/git-config).
No token is placed in arguments, plans or journal records; diagnostic formatting
is redacted and JSON serialization exposes no fields. This does not hide child
environment memory from privileged local processes. Credential helpers and
redirects remain disabled. Authentication does not grant effect authority.

PASS actual local Git --get-urlmatch tests for exact URL matching and rejection
of foreign host, other repository and path-prefix lookalike, plus token/destination
validation and redaction. No authenticated HTTPS request was executed. Push
executor, controller and CLI credential plumbing remain pending.

ExecuteAuthenticated now carries the same destination-bound credential through
remote preflight, the push child and independent readback. Local bare-repository
initialization and ancestry inspection receive no credential. Missing, zero-value
and foreign-destination credentials are rejected before repository/network work.
Existing unauthenticated local execution remains available with unchanged effect
approval rules. PASS full gitpush regression (15.539s), credential entrypoint
checks (0.447s), vet and documentation checks. This is local regression and
credential binding evidence, not an authenticated network push. Controller/CLI
credential propagation remains pending.

Controller PreparePush, ExecutePush, ReconcilePush and RecoverPushLease now accept
one optional explicit destination-bound credential. It is carried through remote
reads, execution and reconciliation without entering the journal or approval
identity. Omission preserves local/unauthenticated operation; an explicit nil,
zero-value, multiple or foreign credential fails validation. Creation of a push
intent and lease-recovery intent validates supplied scope before append. The CLI
still needs the explicit credential selector; authenticated network qualification
remains NOT RUN.

CLI authentication is now wired end to end: prepare-push, push and
recover-push-lease accept optional trailing TOKEN_ENV. The explicit
`reconcile-push RUN [TOKEN_ENV]` command supports authenticated read-only recovery.
Omission preserves unauthenticated/local behavior. The variable name is validated;
there is no automatic ambient-token fallback. Credential values never enter the
preview. Destination-bound credential validation precedes controller execution.
PASS selector/local-destination rejection tests and existing real local CLI flow;
full live authenticated HTTPS push remains NOT RUN.

PASS actual Git over local TLS (1.105s): Git consumes the child-environment scoped
header, reads a local reference advertisement, and returns a sanitized failure
when the server sends HTTP403 with credential-like text in its body. A test-only
private destination replacement and per-command fixture CA let the real Git
OpenSSL backend reach httptest. Production destination restrictions are unchanged.
This tests authentication transport/read/error handling, not GitHub permissions
or a smart-HTTP push mutation. Windows Git/OpenSSL is the executed scope.
