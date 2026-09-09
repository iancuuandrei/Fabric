# Governed draft PR

The draft workflow is implemented locally through the HTTP adapter, controller
and CLI. Local evidence includes simulated-host integration, real TLS socket
failure and controlled child-process exits. Live GitHub qualification is NOT RUN;
this repository has not been published. See [evaluation status](../evaluation/status.md).

## Exact proposal and authority

A versioned plan binds the complete confirmed push plan and intent, repository,
base branch and expected base commit, title, body and fresh nonce. Run, original
repository and implementation-plan identities come from the push envelope.
Controller replay independently requires PUSHED, confirmed push and resolved push
lease recovery. Push plans are compared by validated content identity, including
nullable expected-old commits, rather than pointer identity.

The initial adapter supports branches in one public-GitHub repository. The push
destination must be exactly its HTTPS URL, optionally ending in `.git`. Base and
head are distinct full branch refs. Commit lengths match the local object format;
this does not establish provider support for every format. Fork and Enterprise
routing are unsupported.

Title is nonempty and bounded to 256 UTF-8 bytes; body is bounded to 32768 bytes,
with no CR or NUL. A reserved HTML comment containing the full draft effect ID is
appended to the exact body. Supplied bodies cannot forge that marker, including
case variants. The marker aids correlation; it is not authentication or provider
idempotency. Changed text, branch, commit or nonce requires new approval.

The [GitHub creation API](https://docs.github.com/en/rest/pulls/pulls?apiVersion=2026-03-10)
receives the exact title/body, head/base branch names, `draft: true` and
`maintainer_can_modify: false`. The API selects branch names rather than atomically
pinning their commits. Preflight and readback therefore remain mandatory.

## Controller lifecycle

Preparation holds the workspace lease, checks the pristine committed candidate,
observes the base commit and verifies both branch tips. It returns a preview
without persisting a creation intent or sending a POST.

Execution validates the preview and separate exact authorization, acquires the
lease, rereads the journal and candidate, and appends `draft.intent` before host
execution. State becomes DRAFTING/UNKNOWN. An existing intent cannot be dispatched
again. The client rechecks both tips before POST; a changed tip prevents dispatch.
The controller attempts read-only reconciliation after creation, even when the
creation response was lost. It records `draft.observed` with hosted evidence,
local candidate freshness and an effect-bound observation hash.

HANDED_OFF requires a valid hosted observation and the exact local candidate.
Incomplete evidence remains UNKNOWN and blocks unrelated effects. Absence never
establishes NOT_APPLIED or allows an automatic retry. A transport error can coexist
with a later confirmed receipt; command exit status and journal outcome must be
interpreted separately. Reconciliation of a confirmed terminal draft is rejected.

## HTTP and observation boundary

The client uses only `https://api.github.com`, explicit in-memory bearer credentials,
API version `2026-03-10`, a 30-second request timeout, no ambient proxy and no
redirects. Credentials are absent from plans, receipts and raw transport errors.
The client constructs its own HTTP transport rather than cloning the mutable
process-wide default. A replaced default transport cannot supply a custom dialer
or disabled TLS verification. Regression coverage exercises both a weakened TLS
default and a replacement RoundTripper of another type.
Read requires HTTP 200; creation requires HTTP 201. JSON media type, declared and
streamed byte bounds, and complete declared length are checked.

Responses must satisfy the one-MiB canonical JSON domain: no duplicate keys,
invalid Unicode, trailing data, floats, unsafe integers or non-ASCII member names.
Unknown API fields are allowed only within that domain. Required fields use exact
names and cannot be missing or null. Unrelated list entries may have null bodies.
Unsupported API shapes fail closed rather than being silently normalized.

[Exact reference reads](https://docs.github.com/en/rest/git/refs) escape each path
component and require the requested ref, a commit object and nonzero lowercase
SHA. Both preflight tips must match the plan. These sequential reads are not atomic.

The creation response is validated, then its number is independently read. Every
numbered read binds returned number, canonical HTML URL and both repositories to
the requested locator. Plan validation additionally requires open draft state,
exact title/body/marker, branches and commits. An internally consistent response
for PR 8 cannot confirm a request for PR 7.

## Read-only reconciliation

Reconciliation scans all repository PR states and branches, sorted by creation
ascending, with 100 entries per numbered page. It continues to an empty page,
including after a short nonempty page, and never follows response-supplied URLs.
The bounds are 2000 entries plus an empty-page probe and two minutes overall.
The [list API](https://docs.github.com/en/rest/pulls/pulls) supplies pagination.

Missing or malformed pages, repeated PR numbers, exhausted bounds and multiple
marker matches prevent confirmation. A sole marker match is independently read
and validated against the full plan. Pagination is not an atomic hosted snapshot;
concurrent changes can prevent confirmation, and an empty result is not proof of
non-execution. No reconciliation path sends a POST.

## Interrupted lease recovery

Recovery requires fresh explicit workloads-stopped evidence, the retained token,
exact workspace/source identity and a predecessor-bound recovery intent. That
intent is appended before kernel lease adoption. Recovery observes without
creating, then records lease release separately from the draft outcome.

An interrupted recovery requires a fresh successor approval. This includes a
confirmed draft whose lease release is unresolved. Old creation or recovery
approvals cannot authorize the successor. No timeout or PID guess replaces the
explicit quiescence evidence and kernel exclusion check.

## CLI

- `prepare-draft RUN REPOSITORY BASE_REF TEXT_JSON TOKEN_ENV`
- `draft RUN PREVIEW_JSON INTENT_ID ACTOR TOKEN_ENV`
- `reconcile-draft RUN TOKEN_ENV`
- `prepare-draft-lease RUN EVIDENCE workloads-stopped`
- `recover-draft-lease RUN PREVIEW_JSON INTENT_ID ACTOR TOKEN_ENV`

TEXT_JSON requires exact `title` and `body` fields. TOKEN_ENV is a variable name,
not its value; there is no implicit GH_TOKEN fallback. The CLI checks journal/root
binding and exact preview approval and prints the latest snapshot on execution
errors. The generated [CLI reference](../reference/cli.md) owns argument details.

## Executed evidence and remaining qualification

PASS local tests cover exact approval and payload binding, malformed/ambiguous
JSON, substituted locators, branch drift before POST, response bounds, redirects,
cancellation, credential redaction, pagination and multiple marker matches.

PASS controller integration uses a real workspace, commit and journal with a
simulated host and synthetic prerequisite GitHub push receipt. Lost creation
response and unavailable observation remain UNKNOWN; later reconciliation reaches
HANDED_OFF with one creation. Windows child processes exit with code 23 while
holding the lease, after recovery intent and after draft observation before release.
Fresh successors recover; stale approvals fail; final token removal is verified.

PASS an actual local TLS server records creation then closes the connection before
responding. Read-only reconciliation finds it with exactly one POST. This test also
passes under race detection. Request routing to the local server exists only in
the test transport. The readback-locator regression was reproduced before its fix.

NOT RUN: live GitHub permissions/publication, complete CLI-to-host qualification,
controller termination during active HTTP traffic, and power-loss durability.
Controlled exits at completed boundaries do not establish these properties.
