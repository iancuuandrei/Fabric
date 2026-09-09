# Implementation and evidence status

This file retains historical implementation notes. For the public v0.0.0
checkpoint, the bounded current verdict and its limitations are recorded in
[v0.0.0.md](v0.0.0.md). Historical package PASS statements are not a current
full-project, live M2, or release-readiness verdict.

### Anthropic native refusal classification

Anthropic decoders now retain non-content identity/accounting metadata on
terminal errors. Transport records CONTENT_POLICY only for a typed complete
refusal with an accepted exact observed model and bounded output usage. Other
decode failures remain malformed; no refusal content is released and pending
calls cannot be resent. Actual JSON TLS refusal and foreign-model negative
cases passed with race in 2.417s. The expanded six-case JSON/SSE/unfinished-SSE
matrix then passed with race in 2.701s. Complete refusals require accepted model
identity; foreign models and streams missing message_stop remain malformed,
return no content and cannot be resent. Full native error and provider-
cancellation coverage remains open.

The native refusal matrix was extended to ten cases and passed with race in
3.862s. An error event whose arbitrary type is refusal cannot impersonate a
completed refusal, and a JSON refusal above the admitted output-token cap stays
malformed. Both negative cases preserve pending/no-content/no-resend behavior.

Post-classification package regression passed: transport 27.666s, gateway
4.635s, direct runtime 1.033s and documentation checks 0.437s (serialized,
count=1, exit 0). This supersedes the pre-classification package checkpoint
for these packages only; it is not a complete project regression.

### Native terminal diagnostic sanitization

ResponsesTerminalError has the same finite diagnostic boundary: failed,
incomplete and error are retained; arbitrary status strings and nil values use
a constant message. Focused Responses parser and terminal tests passed in
0.531s. This does not add provider-confirmed cancellation support.

Inspection found AnthropicTerminalError.Error concatenated arbitrary provider
status text despite promising a sanitized diagnostic. It now exposes only a
finite allowlist; unknown, empty and nil terminals return a constant diagnostic.
Focused native parser and terminal-error tests passed in 0.289s. This fixes the
diagnostic leak boundary; durable native-outcome classification remains open.

### Combined transport, gateway and access regression

After adding the matched success, failure and privacy matrices, the complete
three-package race run passed: providertransport 47.744s, providergateway
13.063s and access 5.894s (`go test -race -p 1`, count=1). The process exited 0.
The conformance ledger now reflects the bounded matrices instead of their
earlier NOT RUN status. Overall adapter conformance remains FAIL for the open
cost/raw-usage and native-outcome requirements. This is a local working-tree
checkpoint, not full-project or release qualification.

### Integrated finite response framing checkpoint

Non-streaming support is now integrated from the isolated copy: 18 files were
copied only after each existing destination matched its original SHA-256; the
independent failure-journal work was preserved. Direct routes can select JSON
through configuration, including a model that supports JSON only. Framing is
retained in request identity and durable runtime recovery. Unsupported framing
keeps `CAPABILITY_UNAVAILABLE` through controller routing. OpenCode remains SSE.

Canonical package results: gateway PASS 2.307s, transport PASS 1.652s, direct
runtime PASS 1.005s, config PASS 0.538s and OpenCode PASS 14.122s. Focused controller
provider/routing tests passed in 4.813s. Tests include paired native codec
projections, all three JSON adapters over local TLS, JSON cancellation/wrong MIME,
no-resend behavior, strict output and the literal legacy expectation hash.
The earlier documentation check found a missing method comment; after correction,
the full documentation check passed in 0.345s. These results are not live endpoint,
model-quality or a complete shared auth/privacy/error matrix qualification.

### Failure observation journal checkpoint

The gateway now has a finite, non-content failure observation attached to an
exact admitted call. Focused race tests passed (2.451s): concurrent identical
recording appends one event, foreign/changed/oversize observations reject, and
both the pending call and active access reservation remain intact. The HTTP
transport now records sanitized failure observations before returning, including
local cancellation, bounded HTTP status/code classification and malformed response
diagnostics. Existing receipts omit the new optional field.

The full transport race suite passed in 15.085s after extending HTTP failure
coverage to all three adapters in both JSON and SSE framing (24 cases: statuses
201, 401, 429 and 529). Assertions cover exact credential attachment, durable
classification, no provider-message disclosure, preserved access reservations
and no second POST. The initial expanded run rejected four Chat SSE fixtures
before dispatch because they omitted required stream usage options; corrected
fixtures retain production validation. Direct runtime tests passed in 1.193s,
OpenCode in 15.465s and documentation checks in 0.364s. This is not the complete
auth/privacy matrix or native in-stream error/provider-cancellation qualification.
Bounded status/code mapping sources are in
[provider error provenance](../provenance/provider-error-classification.md).

Additional error-body boundary tests passed with the race detector in 2.392s.
Actual TLS responses exercise a complete 16 KiB body, one byte over the bound,
a truncated declared Content-Length and a complete empty body. Only complete
bounded bodies contribute a digest or machine-code refinement; every case
replays the same durable observation, retains its reservation and blocks resend.
The initial boundary fixture failed at the exact limit because it responded
without consuming the request body; consuming that body before the response
qualified the intended complete-response case. No production limit was changed.

The diagnostic persistence fault test passed with the race detector in 1.323s.
After confirming the pending intent exists inside the actual TLS handler, the
fixture corrupts its temporary gateway file header before returning HTTP 401.
Execute returns AUTH with Recorded=false, exposes no journal details, retains
the access reservation and rejects a second execution without another POST.
The initial injection appended bytes to a SQLite file and did not invalidate
the database; the corrected injection verifies Inspect fails before claiming
fault coverage. This proves invalid-journal handling, not disk-full behavior.

The later full transport race suite passed in 27.380s. HTTP failure coverage now
has 48 cases: three adapters, two framings, bearer/named-header authentication
and four statuses. Each handler verifies the selected credential header, absence
of the other scheme and no secret in request body or URL. Existing journal,
reservation and no-resend assertions remain in every case. These are local
subscription-profile rejection paths; successful API billing, privacy admission
and upstream service acceptance require their separate qualifications.

Successful JSON responses now pass the matched three-adapter/two-authentication
TLS matrix with the race detector (3.140s). All six cases assert selected-header
isolation, exact observed model and token counts, a fully equal durable receipt
before accepting returned content, no secret in either journal and no resend
on a second Execute. This remains subscription-profile local fixture evidence;
the corresponding successful SSE matrix and privacy admission matrix remain
outstanding.

The subsequent successful-response matrix includes both SSE and JSON for all
three adapters and both authentication schemes: 12 cases passed with the race
detector in 9.246s. Each verifies exact model/token values, equality of returned
and durable receipts, completed pending state and no resend. Initial Responses
SSE fixtures omitted mandatory nullable usage detail fields; those were added.
A later transient transport failure occurred before the fixture consumed the
request; handlers now consume request bodies before sending response streams.
Production validation remains unchanged. This closes this bounded text-response
transport matrix, not privacy, API billing or upstream model qualification.

Privacy drift now has 24 transport admission cases across all three adapters
and both framings (race PASS 9.053s): changed training, retention, terms digest
or repository class rejects without network access or gateway journal changes,
while the original reservation remains active. The 12 successful transport
cases also use explicit v2 excluded-training/zero-retention profiles and passed
with race in 7.762s. These prove operator-policy identity enforcement, not that
an upstream provider follows those declarations or accepts confidential data.

The new pinned OpenCode writer/repair/review/local-commit fixture passed its
first actual execution (43.315s, six provider requests). Three real runtime
processes each read the candidate through the owned tool path. Explicitly
authorized proposals drove a real verification FAIL, fixer, fresh verification
PASS, review approval and exact-tree commit in the temporary fixture repository.
Offline runtime recovery and controller re-entry produced no further provider
requests or duplicate commit. The standalone EngOrch checkout was not committed.
The first attempt failed before runtime startup because the fixture omitted the
private state directory; fixture setup now supplies it without weakening runtime
validation. Process readback after success found no remaining Go/controller-test/
OpenCode process. The strengthened exact failure/review input assertions then
passed with the race detector (126.656s, six requests). An intervening race run
failed because a partial test input projection incorrectly used strict whole-
object decoding; the fixture now validates full JSON before reading the selected
fields, with exact verification-context comparison retained. This local TLS
provider fixture is not a model
quality benchmark or a recursive multi-writer integration proof.

### Recursive runtime diagnostic checkpoint

After the fixture envelope correction, the pinned OpenCode recursive explorer
test passed ordinarily (34.528s, four waits, eight provider requests) and with
the race detector (83.031s, six waits, ten provider requests). Both runs started
overlapping parent and child runtimes, verified the child's committed source
read, delivered its exact accepted exploration body to the parent, verified both
composite runtime seals and token usage, and recovered offline without new
provider requests or duplicate accepted-result events. The executable SHA-256 is
`88d2fa691b2d9e32fde6d1039382a850ddf96fe49cd41683c6375fe1dc8ec2a5`.
These are real runtime processes against a controlled local TLS provider, not
commercial model-quality evidence or a full writer/reviewer workflow. The two
earlier failed runs below remain failed; fresh invocations qualified the fix.

Earlier non-streaming work was initially kept isolated after review found missing
configuration and durable framing propagation. The integrated checkpoint above
supersedes that intermediate state.

The fresh opt-in `TestPinnedOpenCodeScheduledRecursiveExplorer` run failed in
37.730s. The retained finite provider diagnostic is `recursive child source result
identity changed` (nine requests, six waits); Pump reported offline recovery and
unavailable OpenCode readback. This is not recursive workflow success. The exact
test process is terminal; process readback found no remaining Go, controller-test
or OpenCode process. No existing invocation was resent.

Inspection isolated the provider diagnostic to fixture decoding: `source_read`
returns a `contextbroker.Response`, whose `Content` contains the source chunk.
The fixture now validates the successful receipt envelope before decoding and
checking the exact source commit and bytes. The fresh runs above qualify this
correction; production source-read validation was not weakened.

Separately, controller scheduled dispatch now obtains its snapshot and journal
head from one validated read through `InspectWithHead`. The concurrent-append
prefix test passed ordinarily (0.167s) and with the race detector (1.246s). This
fix removes a source-confirmed mismatched-prefix race. Accepted-result indexing
and UNKNOWN recovery tests also passed (5.476s). The earlier full controller
suite predates this edit.

This file owns current maturity. Specifications own accepted contracts and ADRs
own rationale. Status is local development, not a released v1.0.0 product.

The September 7 revised objective adds mandatory independent access profiles,
privacy admission, budgets and a second real runtime path. Access primitives and
exact invocation checks now exist, but controller admission across every role
and the OpenCode tool workflow remain incomplete. The
[model access gap audit](model-access-gap.md) records the original gaps; the
current table below distinguishes implemented components from workflow proof.
Historical milestone sections record their execution-time scope and do not
override this table. No final-tree release qualification is claimed.

On September 8 the ordinary internal Go sweep passed every package except the
controller, whose aggregate three-minute test budget expired. A subsequent full
controller run with a ten-minute budget passed in 340.382 seconds. These are
development checkpoints before ongoing host/admission changes, not a final-tree
or race-detector qualification.

The [agent mailbox baseline](agent-mailbox-performance.md) measures durable
message sends and bounded-page reads against real local journals. It identifies
history-dependent replay allocation costs. The [runtime concurrency evaluation](agent-concurrency.md)
separately records the pinned OpenCode process matrix; neither evaluation
establishes external model quality or a complete hierarchical agent workflow.

The [persistent scheduler pump baseline](scheduler-pump-performance.md) adds
real-journal orchestration batches with 16 tasks and simulated 2ms runtime calls.
All nine batches passed exact-once dispatch checks. This isolates neither model
speed nor any single journal cost; additional workers increased allocations and
showed diminishing throughput gains in the measured short workload.

The operator CLI now accepts an exact scheduled OpenCode explorer interrupt
request. Its local regression rejects unsupported or missing turns without
changing the controller, agent tree, agent-control or scheduler journals.
The serial CLI suite passed in 26.333 seconds and documentation checks in
0.323 seconds at this checkpoint. AgentControl request/replay tests separately
passed ordinary and race checks. The controller watcher subsequently passed
focused ordinary (9.724 seconds), race (19.178 seconds), and vet checks. Its
actual helper-process test writes a durable request from another OS process;
the owner observes it and cancels only its child context. Tests also cover
pause/cancel availability and journal corruption cancelling the child while
surfacing an error. This establishes local cross-process notification, not
actual runtime teardown: request admission and `signal_delivered` are not
`confirmed_local_stop`.

Actual pinned OpenCode interrupt-driven teardown subsequently passed in
8.423 seconds. The fixture uses a local TLS planner and a separately blocked
explorer provider. Exactly one explorer request was admitted and canceled;
the exact shutdown receipt proved MCP/proxy handler termination, root-process
reaping and broker closure. Gateway/scheduler outcomes remained unresolved,
and access/TaskPool authority stayed active. Offline claim recovery made no
additional explorer request. This proves local owned-process interruption,
not provider-confirmed cancellation, descendant containment or task success.

The v3 queued composite variant subsequently passed in **9.134 seconds**, with
its focused live race test passing in **20.897 seconds**. It bound the exact
`serial-fifo-v1`/32 recorder policy, admitted one blocked provider request and
proved the composite owner's local-stop receipt, handler termination, root reap
and broker closure. The recorder remained unused, and gateway/scheduler outcomes
stayed UNKNOWN with access and TaskPool reservations retained. Offline claim
recovery did not resend. This qualifies interruption before tool execution;
cancellation while a composite tool callback is active is not established by
this fixture. The legacy interruption fixture remains separate.

The pinned OpenCode scheduled two-turn workflow subsequently passed locally
in 26.639 seconds, with a focused live race run passing in 39.468 seconds.
The production controller completed two distinct turns using four requests to
a local TLS provider fixture; journal-only recovery and reconciliation made
no additional requests. A compact, durably bound private-state namespace
resolved the reproduced Windows startup failure: the old private path reached
254 characters and failed, while the compact 180-character path admitted the
same configuration. This is actual runtime/controller evidence with a fixture
provider, not external model quality, recursive agent MCP or release proof.

The six agent operations now have a controller-bound tool projection with
immutable caller/claim identity, topology restrictions and bounded arguments.
Its focused controller suite passed in 20.033 seconds, including an actual
authenticated MCP HTTP call. HTTP and concurrent-read race tests passed in
8.750 seconds. Valid JSON formatting is normalized; duplicate keys and identity
overrides are rejected before mutation. This is callable transport/component
evidence, not a model-driven recursive workflow: runtime catalog wiring,
transcript receipt validation and complete result delivery remain outstanding.

A subsequent composite HTTP fixture passed in 2.196 seconds: real source
broker and caller-bound `send_message` share a transport recorder while keeping
their authoritative journals separate. The test verifies both backend results,
the complete recorded call/result set, owner identities, and rejection of a
substituted transcript. This does not yet wire the composite into OpenCode's
runtime transcript/seal path. The separate agent source-receipt verifier suite
passed in 10.085 seconds and supports verification after terminal completion.

The composite OpenCode transcript decoder subsequently passed focused ordinary
(0.465 seconds), race (1.968 seconds) and vet checks. It reads receipt state and
head atomically, preserves provider/MCP identity domains, and rejects ambiguous
receipt matches. The new synchronous composite dispatch passed an HTTP fixture
test in 1.272 seconds and, with real context-backend verification, a race run in
2.661 seconds. Tests establish intent-before-POST, two-owner correspondence,
journal-only recovery, backend rejection and no resend after a lost response.
The agent backend in that dispatch fixture is simulated. Full runtime wiring,
composite process sealing and recursive model execution remain incomplete.

The dispatch regression was then extended to hold the first POST open while
attempting a concurrent duplicate. Both successful-response and lost-response
cases retained exactly one POST; journal-only recovery rejected a foreign
dispatch path. This focused race run passed in 2.715 seconds. Separately, the
controller-built backend verifier passed a mixed HTTP test in 2.533 seconds
using real context and AgentControl journals, including caller and owner
substitution rejection.

The versioned composite runtime journal contract passed focused checks in
1.959 seconds, the full runtime package in 10.016 seconds, focused race checks
in 3.483 seconds and vet. Legacy completion explicitly rejects composite turns;
the separate composite seal was not wired at that checkpoint. These tests did
not claim execution.

Subsequent development wired the composite catalog, backend verifier, v3 runtime
journal and separate seal path into scheduled dynamic explorers. Actual pinned
OpenCode composite qualification then **passed in 11.086 seconds**, with the
same focused live race test passing in **19.533 seconds**. The local TLS fixture
executed `source_read` and `list_agents` sequentially, retained both authoritative
owner receipts, and made exactly three provider requests. The v3 terminal seal,
closed broker and authoritative usage of 35 input / 10 output tokens were checked.
Journal-only `Execute` recovery and scheduler reconciliation made no additional
provider request. This proves the mixed runtime workflow with scripted provider
responses, not external model quality or recursive child execution.

Qualification exposed and corrected two semantic identity errors: provider tool
names require the exact `engorch_` namespace, and provider argument SHA-256 must
remain separate from the domain-separated MCP receipt digest. Focused composite
ordinary and race suites, vet and documentation checks passed after these fixes.
Earlier simultaneous calls also exposed the composite MCP listener's
one-active-call limit: the second request received HTTP 429 before recorder
admission. A bounded FIFO waiting queue now preserves serial callbacks while
accepting simultaneous requests. Fresh scheduled explorers bind `serial-fifo-v1`
with at most 32 waiting calls; old zero-queue bindings remain unchanged on replay.
Queue cancellation, deadlines, duplicates, capacity and shutdown passed ordinary
and race checks, as did runtime/controller binding and legacy recovery checks.
Failed invocations remained unresolved and were not automatically retried.

The actual same-generation mixed OpenCode fixture subsequently **passed in
13.365 seconds**, with its focused live race test passing in **19.702 seconds**.
`source_read` and `list_agents` appeared in one provider generation, followed by
the final response: exactly two provider requests. Both owner receipts, the
durable queue policy, composite terminal seal, 35/10 provider token usage,
broker closure and offline recovery without resend were checked. Receipt
arrival order is not assumed to equal provider tool order. This is bounded
local fixture evidence, not a 32-call throughput or external model-quality claim.

The runtime benchmark runner now records a Git-tracked plus nonignored-untracked
development source inventory before compilation, after compilation and after
the matrix, alongside the compiled binary and Python identities. Identity-mode,
Python syntax, PowerShell syntax and an isolated Git output-path check passed.
The updated matrix then passed **12/12 executions** across concurrency 1, 2 and
4, with four-task batches taking 29.181, 16.312 and 10.096 seconds respectively.
All three source-inventory snapshots matched. The runtime concurrency report
above retains raw samples and exact source/binary identities; these are local
source-tool fixture measurements, not mixed-queue throughput or model quality.
The snapshots detect observed source drift; they are not hermetic build or
committed-source receipts.

Atomic AgentControl wait pages now carry their selected journal head into new
MCP source receipts. Agent-tool regressions passed in 43.212 seconds; focused
wait/activity/receipt race checks passed in 44.739 seconds, followed by vet and
documentation checks. Tests cover a later append between page selection and
receipt construction, strict empty-page timeout evidence, legacy timeout
recovery and rejection of substituted prefixes. These are component checks;
accepted child-result body delivery remains a separate unfinished step.

The body-free accepted-result index then passed the full AgentControl suite in
3.828 seconds and its race suite in 9.193 seconds. Concurrent identical calls
converge on one result entry and one notification for each of the parent and
child; changed references for the same turn are rejected. The shared MCP wire
limit and exact envelope fixture passed focused context MCP tests in 2.968
seconds. Controller recovery integration and result-body delivery were not yet
qualified at this checkpoint.

Controller index integration subsequently passed initial/follow-up and UNKNOWN
recovery tests in 5.009 seconds, and their race run in 9.410 seconds. The recovery
fixture uses validated configuration v2, durable planner admission and an
accepted deterministic exploration; it repairs the missing index without
creating a provider runtime journal. Agent-tool regressions passed in 44.726
seconds after the initial versioned wait projection. Accepted body delivery,
its adversarial pagination cases and the updated actual runtime flow still
require qualification.

The actual pinned OpenCode same-generation mixed-tool fixture then passed in
10.404 seconds with the accepted-result index enabled. It checked two provider
requests, the exact controller result reference, one notification per audience,
absence of the semantic body from AgentControl storage and unchanged index
head after offline runtime/scheduler recovery. This is local scripted-provider
evidence; it does not yet prove a parent runtime consuming a spawned child's
body. The first body-delivery component fixtures failed because they attempted
to record an OpenCode result without its required provider receipt; those
fixtures must be corrected before body-delivery claims.

After using the validated deterministic v2 fixture, parent/direct-child body
delivery and exact-wire pagination passed in 9.498 seconds and under the race
detector in 26.464 seconds. Two maximal escaping cases produced 899,761-byte
and 899,478-byte MCP pages under the 1,048,576-byte bound, with no lost result
between cursors. The tests reject changed bodies, changed controller-event
references and downgrade to legacy activity-only receipts. Vet passed for
control, AgentControl, context MCP and toolbridge. Grandchild omission,
follow-up body delivery and actual recursive runtime consumption remain
separate qualification work.

Additional authority, follow-up and historical-prefix cases passed under the
race detector in 25.827 seconds. They verify explicit grandchild body omission
without dropping its activity, distinct identities for follow-ups with identical
text, and rejection of substituted controller prefixes, turns, admissions and
activity sequences. The actual recursive runtime scenario remains unqualified.

Before adding the new recursive fixture, the full controller regression passed
in 429.882 seconds: 231 passed test/subtest cases, nine skipped cases and zero
failures. Raw output is retained at
`.local/control-regression-accepted-results.jsonl`, SHA-256
`e60844cab201e606bdcd626595be8e4a722c98c9a54ee4fdbf375baaad74a5a9`.
This is the default local suite; opt-in runtime checks are separate. The newly
added recursive fixture then failed preflight because its fake planner cannot
use the shared TaskPool. Its planner setup is being corrected before live
execution; the earlier regression does not qualify that new fixture.

A built Windows CLI also passed `init`, `doctor` and fake-runtime `plan` in a
fresh committed repository with PATH restricted to the selected Git executable
directory. Evidence is retained under
`.local/standalone-smoke-a4bf3510c26843929ad8e8314735cde4/smoke-result.json`;
binary SHA-256 is `88b725bbd0bd4734fcb863e610fd3c801bf5096c17b5066416a3f9f7435f2156`.
This checks standalone planning without development runtimes on PATH, not
external model execution or a clean-machine release installation.

The latest seal corrections bind the broker, listener and verified process
launch through their constructors and bound the root-process reap wait by the
caller context. Focused tests, race checks and the pinned source/candidate
probes passed after those changes. This proves in-process ownership for the
tested fixtures; it does not establish OS containment or a hard disk-I/O deadline.
The controller regression suite passed in 370.606 seconds before independent
review corrections. Terminal-append evidence, planner source binding and
crash-window accounting corrections subsequently passed focused tests (3.824
seconds), broader focused regressions (31.136 seconds), vet and independent
re-review. A complete heterogeneous model run remains unqualified.

The finite provider layer now includes Responses, Chat Completions and Anthropic
Messages. Direct routes perform structured inference; coding tool loops belong
to the selected runtime. The actual pinned OpenCode Execute path has completed
local TLS-provider fixtures with MCP source reads, sealed receipts and offline
recovery. This does not qualify external Muse access or model quality. Controller
admission, shared capacity and agent-tree integration are still being completed.

| Area | State | Evidence |
| --- | --- | --- |
| Research | Complete for initial decisions | Five independent Luna assignments, documentation synthesis and pinned donor inspection |
| Canonical JSON | Implemented | Invalid Unicode, duplicate keys, integer domain, ordering, hash separation and fuzz seeds |
| Journal | Implemented | Tamper/torn-tail rejection, validator atomicity, concurrent append and stale-lock tests |
| Planning kernel | Implemented | Exact config/input/model/plan binding and approval replay tests |
| Writer proposal boundary | Implemented locally; broader qualification partial | Explicit routing, authenticated candidate-tool/repair fixtures, exact file proposal conversion and stale-response rejection; [contract](../specifications/writer-proposals.md) |
| Optional explorer | Runtime and CLI implemented; broader qualification partial | Authenticated historical-context explorer/writer fixture, receipt accounting, stale-state rejection; [contract](../specifications/exploration.md) |
| Effect envelopes | Implemented for local effects | Exact intent/approval binding and UNKNOWN handling for workspace, file and RI lifecycle execution |
| CLI/config | Implemented | Real temporary Git repository planning/approval/resume; unknown config and stale generated reference checks |
| Git discovery | Implemented | Real commit/tree discovery, dirty source and unborn repository tests |
| Worktree controller | Implemented | Real Git materialization, lease exclusion, reciprocal registration, candidate hashes and journaled reconciliation |
| File-proposal executor | Implemented | Whole-candidate prediction, exact approval, binary create/replace/delete, stale-state rejection and partial/lost-receipt tests |
| Verification execution | Implemented locally | Candidate-bound process execution and receipt tests; see milestone evidence below |
| Real model runtime | Partial | Authenticated planning, writer repair, positive and negative-review repair fixtures executed; broader tasks remain unqualified |
| Access admission | Codex subscription controller integration implemented; workflow qualification partial | Planner, explorer, writer, fixer and reviewer bind durable reservations and runtime evidence. Focused replay tests, vet and the pre-correction full controller regression passed, including mixed fake-planner accounting. Independent review corrections passed tests and re-review; OpenCode and API access remain incomplete |
| OpenCode runtime | Recursive explorer controller path locally qualified; full engineering workflow incomplete | Pinned parent/child processes passed scheduled spawn, bounded wait, accepted-result delivery, source read, composite seals, usage and offline no-resend checks ordinarily and with the race detector. The provider is controlled local TLS; external model quality and the complete write/repair/review workflow remain unqualified |
| Source and candidate tools | Shared cores and MCP execution locally qualified; controller integration incomplete | Real candidate tests cover changed bytes, deletions, pagination and drift rejection. Pinned OpenCode separately executed source_read against a dirtied source and candidate_read against a leased modified worktree; each synthetic provider received the complete durable receipt. Both transcripts passed the tool-turn decoder and owned shutdown, with the candidate lease held through sealing |
| Provider subcall accounting | Finite protocol adapters and local transport implemented; external provider qualification pending | Responses, Chat and Anthropic adapters retain requested/observed identity, usage and terminal receipts. Pinned OpenCode Execute exercises the production proxy/transport against a local TLS provider. External pricing, provider availability and model quality remain unqualified |
| Run lifecycle | Controller and CLI implemented; all-route dispatch ordering in progress | Pause/cancel requests, explicit quiescence settlement, retained UNKNOWN work and pause resume pass focused controller and CLI integration tests; [contract](../specifications/run-lifecycle.md) |
| Agent tree and task pool | Recursive explorer integration locally qualified; broader workflow qualification partial | CLI and journal tests cover queued turns, mailboxes, exact accepted-result references and UNKNOWN recovery. Actual parent/child OpenCode explorer runs passed with two workers and shared capacity two. Capacity-one parent waiting cannot progress a child; no slot is silently released. Recursive writers and arbitrary hierarchy workloads remain unqualified |
| Codex host integration | Thin local plugin and host observation implemented; deployment unqualified | Skill wraps public CLI; doctor distinguishes host hints from verified sandbox evidence. No production sandbox verifier or Codex UI installation is qualified; [contract](../specifications/host-environment.md) |
| Lexical search | Implemented locally; workload qualification partial | Rust workspace and actual Rust Go/CLI lifecycle tests passed with path/type filtering before limits/cursors; retained synthetic scale measurements are not full-controller or model-economics proof |
| Local packaging | Development packager implemented; final-tree qualification pending | Native Windows Go/Rust builds and 15-file payload verification passed; tamper, extra-file, false commit label and concurrent source-drift rejection tested. Final stable-tree and other-platform packages remain NOT RUN |
| Rust RI/SCIP | Implemented locally, qualification partial | Snapshot/query tests and real scip-go producer/import/publication/runtime-broker navigation |
| Procedures | Seven text packages implemented | Structural validation PASS; model behavior and cross-host discovery NOT RUN |
| Context economics | Accounting implemented; economics NOT_RUN | Journal-bound byte/tool accounting; no context-cost qualification claimed |
| Local commit and recovery | Implemented locally; crash qualification partial | Exact objects/ref/index, distinct approvals, controller/CLI and bounded process-exit fixtures; [contract and limitations](../specifications/local-commit.md) |
| GitHub handoff | Implemented locally; hosted qualification NOT_RUN | Draft transport/controller/CLI and local recovery fixtures; no external publication |
| Draft PR | Controller/CLI implemented; qualification partial | Exact approval, bounded HTTP, branch preflight, read-only reconciliation and actual process-exit lease recovery with simulated host; [contract](../specifications/draft-pr.md) |
| Git push | Controller/CLI locally qualified; interruption recovery partial | Local bare SHA-1/SHA-256 execution, controlled receipt omission, read-only reconciliation and stale-approval rejection; [scope](../specifications/git-push.md) |
| Performance qualification | Partial local measurements | [Search concurrency](search-concurrency.md), [task-pool components](taskpool-performance.md) and [actual OpenCode fixture concurrency](agent-concurrency.md) retain bounded evidence. Benchmark artifact binding is under review; external model economics and production capacity remain unqualified |

## Kernel milestone validation, 2026-09-06

Executed on Windows amd64 with Go 1.27.1 and the installed GCC race-detector
toolchain. `scripts/check.ps1` completed Go tests, vet, race tests, module
verification and formatting. Documentation tests checked local links, ADR IDs
and statuses, exported Go declarations, package docs and generated CLI reference.
Additional journal/CLI regression tests and the binary build passed afterward.

Bounded fuzz campaigns used two workers and a three-second requested budget:
canonical normalization executed 55,748 cases; journal replay executed 161,909.
Both returned PASS. These are parser robustness observations, not throughput
benchmarks. They do not establish absence of all bugs or real-runtime readiness.

Rust 1.94.1 is pinned with formatting/lint components; its future workspace gates
are declared in the check script. Rust code/tests are NOT_RUN because RI has not
been implemented. Hosted/provider/security-isolation qualification is NOT_RUN.

## Writer workspace validation, 2026-09-06

Go tests, vet, race detection, module checks and formatting passed after workspace
integration. Real temporary repositories demonstrate preservation of dirty source,
raw committed-blob materialization without checkout hooks, stable fingerprints,
writer exclusion, branch substitution rejection, hardlink rejection and explicit
reconciliation after lost confirmation. Non-pristine reconciliation stays UNKNOWN.

Additional Windows junction and raw Git symlink-tree tests passed. Creating an
ordinary directory symlink is unavailable under this host's privileges, so that
specific test is SKIPPED. Junction/reparse and hardlink results are independently
executed; they do not establish complete OS sandboxing. Tests never modify LexAI.

The next coherent step completes Phase C with exact approved file proposals,
atomic rooted file effects, partial-effect reconciliation and required verification
against the resulting candidate. Real runtime, RI, semantic import, procedures,
GitHub handoff and full evaluation remain in the active goal.

## File-effect validation, 2026-09-06

Exact file proposals and their CLI are implemented. Full Go tests, vet, race
detection, module verification and formatting completed successfully. The binary
build and final CLI/reference/documentation checks also passed. Tests exercise
binary create/replace/delete, unchanged canonical source, stale approvals, whole
candidate drift, protected paths, lost confirmation, NOT_APPLIED observation and
injected partial writes. Partial state remains UNKNOWN and blocks new effects.

File-effect reconciliation observes without retry. Separate explicitly approved
recovery now recognizes and completes partial writes, including known temporary
file prefixes. Targeted recovery tests passed. Required verification is now
integrated with the controller and `verify RUN`; its distinct evidence follows.

## Verification controller validation, 2026-09-06

PASS: targeted controller tests execute real local child processes covering exit
zero, exit seven, source mutation during a successful process and an unavailable
tool. Only the unchanged successful candidate enters READY. Replay tests reject
dropped required checks, skipped check indices and duplicate pending launches.
CLI, invocation runner and documentation tests passed independently.

Interrupted verification now supports continuing only unstarted checks and
explicit operator closure of uncertain attempts. Targeted tests prove retention
of completed evidence, rejection of stale/incomplete attestations, preservation
of unknown outcomes and fresh identities for subsequent explicit attempts.
The completed check script, including these recovery changes, passed Go tests, vet, race detection,
module verification and formatting. Real model, Rust RI, SCIP and hosted GitHub
qualification remain NOT_RUN.

Full-goal completion requires all later phases, fault injection, unrelated
repository evaluations, docs checks and exact evidence. The kernel milestone
does not satisfy the entire goal. No push, PR or release has occurred.

## Codex transport validation, 2026-09-06

PASS: bounded protocol tests cover duplicate keys, malformed envelopes, request
ID mismatch, approval denial, cancellation while blocked on reads/writes and
connection invalidation. Runtime tests reject observed provider/effort
substitution and preserve absent observations as null.

PASS: an opt-in handshake with installed Codex CLI 0.153.4 used a fresh temporary
configuration home and returned matching server metadata. It created no thread
and dispatched no model turn. This is transport compatibility evidence only;
the full AgentRuntime adapter and real model qualification remain incomplete.

PASS: the complete Go check script finished after these transport and runtime
changes, including tests, vet, race detection, module verification and formatting.
The installed-server probe is opt-in and was executed separately; ordinary test
runs explicitly skip it. Rust checks remain NOT_RUN.

## Codex lifecycle validation, 2026-09-06

PASS: scripted-peer tests verify durable intent before thread and turn dispatch,
exact routing, final-answer selection, duplicate-execution rejection, uncertain
lost-response handling and continuation by recorded-turn readback. Failed turns
retain terminal evidence without a successful result. Model rerouting, foreign
completion IDs, partial output and duplicate item identities are rejected.
Targeted package tests and race detection passed.

PASS: the completed repository check script also passed Go tests, vet, race
detection, module verification and formatting with the lifecycle adapter present.

The lifecycle adapter has not dispatched a real model turn. Authenticated process
provisioning, qualified tool authority, controller integration, targeted
asynchronous cancellation and full-goal evaluation remain incomplete.

## Codex host configuration validation, 2026-09-06

PASS: the installed CLI was started with a separate configuration home, fixed
CLI policy overrides and a selected environment. Protocol readback confirmed
26 disabled features, suppression of host skill discovery and an empty MCP
inventory. It accepted a local thread with explicitly empty environments and no
provider model fallback. No model turn was dispatched.

Live testing exposed the notification timestamp field and a forced-on UnifiedExec
selector. The transport now supports the installed timestamp schema; the policy
uses ShellTool denial and explicit empty environments rather than the ineffective
selector override. The installed binary also required raising its hash size limit
from 256 MiB to 512 MiB. These changes are grounded in executed observations.

PASS: changed-package tests, race detection, vet and documentation checks.
Configuration admission is not adversarial tool-boundary qualification. Real
authenticated generation, controller wiring and approved repository tools remain
incomplete. Earlier full-suite results apply to their recorded prior milestones.

## Authenticated Codex round trip, 2026-09-06

PASS: the installed CLI, admitted host and lifecycle adapter completed a real
`gpt-5.6-luna` / `low` planner invocation. The prompt contained no repository data
and requested a fixed qualification response. The full response matched exactly,
requested/observed routing matched, and a completed-turn result was durably
admitted. Evidence is local in `.local/codex-live-evidence.json` with the retained
attempt journal in `.local/codex-live-attempt-3014128829/runtime.jsonl`.

Two earlier development probes did not admit output: the completion event supplied
summary items. The adapter now reads the existing turn's full output, and a
regression test proves exactly one turn/start followed by thread/read. The passing
probe also verified no auth.json was created in its isolated home. No original
authentication file was modified or copied; only the existing access token and
account identifier were supplied through the external-token protocol.

PASS: authentication-material selection tests, summary readback regression,
changed-package tests, race detection, vet and documentation checks. Unknown
usage remains null; no price, model-quality or complete workflow claim follows
from this small protocol test. Controller CLI integration, approved repository
tools and the remaining original goal phases are still incomplete.

## Codex controller planning, 2026-09-06

PASS: the actual CLI `plan` command completed an authenticated qualification
prompt using gpt-5.6-luna/low and reached AWAITING_APPROVAL with linked host,
runtime-journal and result receipts. A subsequent `resume` returned identical
output. Evidence is `.local/codex-cli-live-evidence.json`; the retained source/run
and runtime journals are below `.local/codex-cli-attempt-3949509033`.

Offline tests reject unbacked Codex plans, binary substitution, missing host
preparation and runtime state roots inside the source repository. This integration
currently supplies objective text only. Approved repository tools and the full
implementation/repair/review workflow remain incomplete.

PASS: the completed full repository check script passed Go tests, vet, race
detection, module verification and formatting after controller integration.
Rust checks remain NOT_RUN.

PASS: committed source listing and reading tests cover pagination, binary bytes,
dirty checkout isolation, global path order and bounded stream parsing. Race
tests and vet passed for repository, Codex RPC and runtime packages. The scoped
RPC handler test proves dynamic-tool responses and approval denials remain
distinct; its private handled marker cannot be supplied by provider JSON.
Documentation checks passed after adding the required stream-method comment.
The repository tool broker and model-facing source access remain NOT_RUN.

Subsequent source-broker milestone (2026-09-07): PASS, authenticated
`TestLiveCodexSourceToolsCLI` with gpt-5.6-luna/low. Both list/read requests and
successful responses are durable, the model returned the exact random committed
text despite a dirty checkout decoy, and completed resume was unchanged.
Evidence: `.local/codex-source-live-evidence.json`, with retained journals under
`.local/codex-cli-attempt-177592483`. Earlier probes exposed missing direct tool
visibility and inaccurate model transcription of base64; these were failures,
not qualification passes. Explicit direct-only exposure and a lossless UTF-8
view resolved the fixture failures. This is not a model-quality benchmark or
proof of native-tool confinement. Implementation/repair/review remains incomplete.

PASS: the full check script completed after source-broker integration: Go tests,
vet, race detection, module verification and formatting. Documentation checks
also passed after the final documentation edits. Rust remains NOT_RUN.

Rust foundation milestone (2026-09-07): PASS, `cargo test --workspace` (five
unit/integration tests and one rustdoc example), `cargo clippy --workspace
--all-targets -- -D warnings`, and formatting. The CodeGraph segmentation port
retains MIT provenance and tests Unicode, acronyms, numeric/short terms and
explicit bounds. Graph tests cover scoped false absence, stable ordering,
forward/reverse lookup, quality preservation and invalid graph rejection.
Go documentation checks passed. This is library-level RI evidence; immutable
snapshot interchange, structural extraction, SCIP and CLI qualification remain
NOT_RUN. No graph performance claim is made.

Snapshot library milestone (2026-09-07): PASS, twelve Rust unit/integration tests
and one rustdoc example, clippy with warnings denied, formatting and Go canonical
and documentation tests. Tests now include expected-source substitution,
unregistered coverage producers, invalid/duplicate provenance, deterministic
snapshot bytes, changed/torn/reordered artifacts and strict canonical rejection.
A shared Go-generated Unicode/control/safe-integer fixture and domain-separated
hash are verified independently by both languages. This is a bounded fixture,
not exhaustive interchange qualification. Snapshot disk publication, CLI queries,
SCIP and structural producer execution remain incomplete.

Query pagination milestone: PASS, fourteen Rust unit/integration tests plus one
rustdoc example and clippy with warnings denied. Verified snapshot handles reject
foreign snapshot/query cursors. Three-page traversal preserves stable edge order,
and empty later pages do not prove absence. This remains library-level evidence;
the agent-facing CLI and disk publication are not yet implemented.

RI process milestone: PASS, fifteen Rust unit/integration tests and one rustdoc
example, including actual subprocess reads and rejection after disk mutation.
The Go client also executed the built Rust binary: a Go-created canonical
snapshot was verified and returned the exact source/hash, then changed bytes
were rejected. Executable substitution and cancellation tests passed. The full
check script now builds Rust and runs this interoperation test explicitly.
No producer/indexer execution or immutable disk publication is claimed.

PASS: the updated full check script completed with Go tests, vet, race detection,
module verification and formatting; Rust formatting, clippy, tests and build;
and the explicit Go-to-Rust process integration test. The fifteen Rust tests and
one rustdoc example passed in that run. This does not complete the remaining RI
producers, storage publication or workflow phases.

Local store milestone: PASS, Go store tests under race detection and vet, plus
the actual Go/Rust integration test using store publication. Cases include
reuse without replacement, corruption, partial staging, linked staging,
independent conflicting destinations and concurrent publication. These are local
primitive-level checks. Controller journal integration and filesystem power-loss
durability are not qualified.

Graphify adaptation milestone: PASS, nineteen Rust unit/integration tests and one
rustdoc example, clippy with warnings denied, formatting, binary build and the
Go/Rust integration test. Four new structural tests use the actual Rust grammar
and executable: qualified generic names, Unicode byte spans, repeated occurrences,
syntax errors, source-hash binding, invalid bytes and traversal exhaustion.
Graphify licenses/notices are retained. This is structural type-occurrence
extraction with PARTIAL semantic coverage; graph import, full structural indexing
and SCIP remain incomplete.

Occurrence milestone: PASS, twenty-two Rust unit/integration tests and one rustdoc
example, plus clippy with warnings denied. New tests cover UTF-8/16/32 coordinate
conversion, invalid scalar boundaries, CRLF, multiline SCIP legacy ranges, source
substitution and unresolved structural occurrence identities. Snapshot occurrence
records and the actual SCIP importer remain incomplete.

Occurrence snapshot milestone: PASS, round-trip preservation and rejection of
wrong producer/path/source hash/range/resolved symbol or duplicate occurrence ID.
Snapshots retain unresolved occurrences and index them by path and resolved
symbol. Status reports occurrence count. The complete Rust suite has twenty-three
tests plus one rustdoc example; clippy and the updated Go/Rust integration pass.
SCIP import and definition/reference role handling remain incomplete.

## RI runtime selection and procedures, 2026-09-07

PASS: the full local check script completed Go tests, vet, race checks, module/format checks, Rust fmt/clippy/tests/build and the configured actual Rust integration tests. Development continued during this run, so this is not a sealed final-tree receipt. Subsequent targeted CLI/runtime, controller-to-Rust, actual scip-go CLI-to-broker and documentation checks passed for their changes. The actual producer broker fixture uses synthetic provider correlation; authenticated model RI navigation is NOT RUN.

Seven portable procedure packages are now implemented. Executed YAML/UTF-8/name/description/length/set checks passed for all seven, and repository documentation checks passed. Behavioral selection/execution and cross-host portability evaluations remain NOT RUN; their cases and evidence requirements are in [procedure qualification](procedures.md). These skills grant no execution authority.

## Authenticated writer fixture, 2026-09-07

PASS: TestLiveCodexWriterCLI with authenticated gpt-5.6-luna/low completed in 17.56 seconds on the local Windows host. A deterministic fixture planner supplied the task; the real writer returned a strict candidate-bound create-file proposal. The controller recorded its runtime receipt and proposal, verified no file existed before separate application authorization, then applied and checked exact writer-newline content in the isolated worktree. Canonical source remained unchanged. This is a bounded protocol fixture, not model-quality or repair qualification.

The first executed attempt failed before model dispatch because the private writer directory was missing. Creating the full intended private host directory before host preparation fixed it; the subsequent new fixture passed. Both attempts remain in local evidence. Scoped missing-executable regression, vet, CLI and documentation tests passed. Changed-candidate tools, repair/reviewer orchestration and full qualification remain unfinished.

## Authenticated modified-candidate context, 2026-09-07

PASS: TestLiveCodexCandidateWriterCLI with gpt-5.6-luna/low completed in 25.44 seconds. The fixture generated a random marker after planning, admitted it through an independently authorized source.txt change in the isolated candidate, and requested a proposal copying those bytes into a new file. Runtime evidence contains successful candidate_list and candidate_read calls, the bound modified candidate and the exact journal head from the controller receipt. The new file remained absent before separate application authorization; afterward its bytes matched the random marker exactly. The canonical source.txt retained base-newline content. This qualifies one authenticated modified-candidate context path, not a failure-driven repair loop or general model quality.

## Authenticated failure-driven repair, 2026-09-07

PASS: TestLiveCodexRepairCLI with gpt-5.6-luna/low completed in 24.05 seconds. A real configured helper process checks generated.txt bytes and initially exits nonzero with a diagnostic. The controller enters REPAIRING; the writer invocation includes that FAIL evidence. The authenticated writer proposes the file correction without applying it. After separate exact proposal authorization, the same configured helper runs again and passes, producing READY under the current verification state machine. The test neither edits the verifier nor accepts a model test-success assertion. This is a one-check, one-file repair fixture; reviewer integration, broader tasks and full release qualification remain unfinished.

## Authenticated optional reviewer, 2026-09-07

PASS: TestLiveCodexReviewCLI completed in 52.34 seconds with explicitly configured gpt-5.6-luna/low writer and reviewer in distinct private hosts/threads. The fixture executes a failing check, authorized repair, the same passing check, REVIEWING, and a runtime-receipt-bound approval to READY. Candidate identity remains unchanged by review. This verifies independent invocation/host state, not independence of model training or a broad reviewer-quality benchmark.

The first attempt failed admission because the reviewer copied the implementation plan ID into verification_plan_id. No approval was admitted. Making the distinct verification identity explicit in the top-level input and instructions fixed the fixture without weakening validation. Live candidate drift, substituted identities and contradictory verdict regression tests also passed. Broader review tasks, negative authenticated verdicts and release qualification remain unfinished.

## Authenticated RI roles, 2026-09-07

PASS: real scip-go 0.2.7 production, Rust import/publication, authenticated gpt-5.6-luna/low writer and separate reviewer, exact runtime RI bindings and successful ri_status/ri_locate/ri_definition calls from both roles. The writer proposed the greeting from indexed source; the fixture separately authorized application and checked exact bytes. The reviewer admitted the unchanged candidate. The configured git-version check proves process execution only; it does not test application behavior. Canonical source remained unchanged. TestLiveControllerScipGoRoles completed in 90.79 seconds on Windows.

The first attempt FAILED because status omitted the required producer IDs and the model guessed invalid identities. Status now exposes the sorted registry IDs from the verified manifest; Go validates bounds/order and tool descriptions point to that field. Rust snapshot and actual Go RI/runtime tests pass. Retained attempts are local private evidence, not release artifacts. This result does not qualify broader tasks, negative-review repair, context economics or the complete product.

## Authenticated negative review and repair, 2026-09-07

PASS: TestLiveCodexNegativeReviewRepairCLI completed in 59.33 seconds on Windows with explicit gpt-5.6-luna/low writer and reviewer profiles. A separately authorized fixture change introduced incorrect content. After git-version passed, the first reviewer requested changes with a finding for generated.txt. The writer received the negative review in its invocation and proposed a correction without applying it. Separate authorization applied the proposal; exact bytes matched the objective. Re-verification and a second review reached READY without reviewer mutation. Usage retained all three hosts in order, each with a completed runtime and matching receipt. Canonical source remained unchanged.

The process check intentionally does not test application semantics. This fixture establishes feedback transport, role execution and governed transitions for one simple defect; it does not establish general reviewer accuracy, autonomous effect permission or release readiness. Private attempt evidence is retained locally.

## Authenticated explorer and historical context, 2026-09-07

PASS: TestLiveCodexExplorerWriterCLI completed in 57.21 seconds with explicit gpt-5.6-luna/low explorer and writer profiles. The explorer read a modified candidate using candidate_list/candidate_read and recorded advisory synthesis. A separately approved file change then made that observation historical. Writer input retained the old candidate identity and candidate_current=false. The writer independently used both candidate tools, proposed the new exact bytes, and the fixture applied them only with separate effect authorization. Runtime receipts and usage covered both hosts; canonical source remained unchanged.

Two earlier attempts with still-current synthesis FAILED the independent-read assertion: the writer answered without both required calls despite receiving the tools. The passing historical-context scenario establishes direct access when current bytes differ from explorer evidence. It does not establish universal instruction adherence, broad synthesis quality, context savings or concurrent exploration.


## Go result-cache diagnostic, 2026-09-07

A full-suite coordinator remained active after its test children exited. Native
GDB attachment captured a running stack through runCache.saveOutput,
computeTestInputsID, search.InDir and filepath.walkSymlinks. The retained local
stack log is go-suite-draft-stacks.log. The coordinator was detached, then stopped
only after checking its executable, start time and absence of children. That run
has no completed whole-suite result and must not be reported as PASS.

scripts/check.ps1 now uses -count=1 for ordinary and race Go tests, requiring actual
execution and avoiding result-cache input processing. This preserves the build
cache. A fresh whole-suite run with that option is in progress; its completion
must be checked before claiming the workaround is qualified. No toolchain source
or canonical repository history was modified for this diagnostic.

The replacement `go test -count=1 ./...` run completed PASS (controller324.011s;
all packages passed). This establishes completion without result-cache processing
for that development run. Later credential TLS tests and measurement tooling were
added during the run; it is not a final-tree release receipt. No active suite
process remains from either run.

## Generic API integration, 2026-09-08

PASS at scoped provider-seal/journal checkpoint: per-generation text digests
include intermediate assistant text; legacy no-provider seals retain an explicit
compatibility projection, while provider-backed seals require the enriched
evidence. Provider process identity distinguishes wire and runtime output caps.
Cleanup after an unsealed failure attempts bounded shutdown and process reaping
without claiming a closed broker or terminal result. Focused seal/decode tests
(8.428 seconds), seal race tests (10.455 seconds), gateway journal race tests
(1.240 seconds) and scoped vet passed. The broader package run still exposed
provider-configuration and executor integration failures outside that checkpoint;
it is not a full runtime PASS.

A subsequent checkpoint passed both `internal/opencode` (16.922 seconds) and
`internal/opencoderuntime` (3.729 seconds). The executor package independently
passed ordinary tests (3.743 seconds), race tests (5.495 seconds) and vet after
the integration fixes. These package results supersede the earlier failures at
that checkpoint; the dedicated test of the complete public executor through a
pinned OpenCode process and local TLS provider remains in development.

PASS: access profile v2 privacy identities preserve v1 profile hashes, distinguish
unknown from excluded training and zero retention, and reject changed terms when
resuming a durable invocation. A per-profile concurrent-invocation override is
enforced within each access policy journal: a saturated profile is denied without
modifying the journal, another profile remains independently admissible, and a
validated terminal releases the correct slot. Access package tests, race checks
(1.556 seconds) and vet pass. This is not account-wide rate limiting and does not
prove provider compliance with declared retention terms.

PASS at package scope: version 2 provider contracts bind protocol adapters,
arbitrary HTTPS endpoints, explicit credential references, public headers,
capabilities and conservative per-call reservations. Chat Completions and
Responses codecs share the outbound transport interface. Fractional Responses
controls are tested through an actual model identity and binding; capability
range bounds use decimal strings to preserve canonical identities. Explicit
observed-model aliases are hash-bound; request model substitution remains denied.

PASS in local TLS fixtures: credential leases and the outbound transport compose
with durable provider-call admission and completion. Tests cover bearer and
subscription header authentication, single-send behavior, redirects, ambient
proxy suppression, cancellation, malformed and oversized responses, and secrets
absent from journals and returned errors. The reviewed journal-append uncertainty
classification is corrected: unreadable, pending or changed gateway state returns
an unresolved outcome without sending. A regression verifies zero sends against
an existing pending call. A Responses TLS integration also passes with fractional
sampling controls, reasoning, a function call and an explicit observed-model alias;
mutation of caller-owned inputs after dispatch does not alter the admitted call.

PASS: the pinned OpenCode Responses source-read probe completes two provider
requests and one durable broker call in 2.96 seconds. It uses typed provider
startup and process identity, the explicit pinned schema projection, exact
request and SSE validation, distinct tool/item identities, synchronous recovery,
and online/offline sealing. The original broker catalog retains numeric argument
bounds removed from the projected wire catalog. This uses a local fixture
provider; it is not external inference or the complete controller executor.
The proxy package separately passes
ordinary, race and vet checks, including actual proxy-to-transport-to-local-TLS
composition, standard local protocol paths with arbitrary upstream paths,
fractional controls and shutdown settlement. Typed provider host startup and
secret-free launch identity also pass focused race and vet checks. Runtime
journal, seal and controller composition remain in development. No live external API model, mixed-provider controller workflow
or final release qualification is established by these local tests.

### OSS composition integration checkpoint (2026-09-08)

The direct provider package now implements one bounded structured inference,
not a coding-agent/tool loop. Its scoped ordinary tests, race tests and vet
passed at the worker checkpoint; local TLS fixtures do not qualify an external
provider. The actual pinned OpenCode Responses source-read probe passed with
two provider requests and one durable broker call after exercising developer
role, text verbosity and the pinned SDK's reasoning-item projection. The public
executor's full process fixture is still being integrated, including exact
session-bound prompt-cache identity.

The first combined `go test ./...` of these edits was **FAIL**: the in-flight
GitHub dependency lacked a transitive go.sum entry, one new exported transport
method lacked documentation, and three provider-proxy tests failed. These are
integration failures, not a passing whole-tree checkpoint; owners are correcting
them. The other packages reported by that run passed, including access, journal,
OpenCode, provider runtime, gateway and transport.

The new CLI `inspect RUN --export-jsonl` validates and serializes the same event
snapshot before emitting any bytes, checks run/repository binding, and does not
write the journal. Its missing/corrupt/cancelled-history test passed (0.113s),
and the existing full fake-runtime plan/approval/resume test with canonical
export/replay assertions passed (19.181s). Default SQLite dispatch and read-only
database opening are a separate in-progress integration; these CLI tests were
run before that backend switch.

### Integrated local checkpoint and remaining broad verification

New journals now use SQLite while existing JSONL remains supported. CLI export
uses one context-aware event snapshot and checks run/repository identity before
output. Export/reference tests passed (0.126s); documentation checks passed
(0.306s). ADR 0006 and the run-journal specification now describe the actual
backend transition.

The actual public OpenCode executor fixture passed at the worker checkpoint
(4.05s): pinned runtime, owned MCP/broker/proxy, local TLS provider, two requests,
one source read, complete sealed result and offline recovery without resend.
Private runtime state versus working-directory separation is still being wired
through the controller and seal, so this is not full configured-role qualification.

Root broad access/CLI/control verification did not complete: access passed and
CLI reported the since-fixed reference mismatch; the control test process later
exited without a captured final report while its Go parent remained childless.
The exact parent was cancelled and that run is NOT RUN TO COMPLETION. The named
commit crash-recovery test was subsequently executed independently with a 90s
bound: all intent/objects/ref/index cases passed in 37.988s. This removes the
specific suspected regression, but does not establish full-suite success.

Search concurrency measurements are recorded in search-concurrency.md. Agent
and orchestration throughput/cost qualification remains pending and must not be
inferred from search or one successful agent integration fixture.
