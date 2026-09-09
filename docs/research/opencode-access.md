# OpenCode access boundary research

Inspected the official [OpenCode Go documentation](https://opencode.ai/docs/go/)
on September 7, 2026. Its subscription is authenticated using an API key. This
invalidates a contract that equates credential references exclusively with API
billing. Access profiles now permit exactly one key reference or runtime session
mode for subscriptions; subscription cost remains unknown in execution receipts.

The same documentation asks independent clients to identify themselves and send a
stable `x-opencode-session` identifier. This is a requirement for the prospective
adapter, not evidence that EngOrch has executed a compatible request. Model
availability and account eligibility require separate live verification.

The official [server documentation](https://opencode.ai/docs/server/) is the next
protocol reference for session execution and observation. Before selecting a
concrete adapter implementation, inspect a pinned implementation revision for
request identity, retries, readback, cancellation and permission behavior. No
OpenCode server or model has been invoked during this research.

## Pinned protocol inspection

Remote HEAD resolved to `57ef3828431790c53f8f333c7ffbfe88770a1812`.
Selected source bytes and SHA-256 hashes are retained under
`.local/opencode-protocol/57ef3828431790c53f8f333c7ffbfe88770a1812/manifest.json`.
The revision carries an [MIT license](https://github.com/anomalyco/opencode/blob/57ef3828431790c53f8f333c7ffbfe88770a1812/LICENSE).
This inspection imports no implementation code into the product.

The [prompt schema and implementation](https://github.com/anomalyco/opencode/blob/57ef3828431790c53f8f333c7ffbfe88770a1812/packages/opencode/src/session/prompt.ts)
accept a caller message ID and explicit provider/model pair. Prompt processing
creates a user message before entering the model loop. The inspected path does
not establish inference idempotency for repeated caller IDs. Therefore a stable
message ID is correlation evidence, not permission to retry an uncertain POST.
The schema also permits file, agent and subtask parts; a narrow adapter should
admit only explicitly supported part kinds. Tool booleans affect session
permissions and are deprecated in favor of session permission configuration.

The [HTTP handler](https://github.com/anomalyco/opencode/blob/57ef3828431790c53f8f333c7ffbfe88770a1812/packages/opencode/src/server/routes/instance/httpapi/handlers/session.ts)
forks asynchronous prompt processing and returns no content. A successful async
HTTP response is therefore not a completed model receipt. Errors can be published
as session events after submission. Completion must be established by correlated
readback of message state and admitted output, with explicit provider/model checks.

Remaining prerequisites: inspect message-read schemas and terminal markers,
session creation identity/recovery, server authentication and isolated configuration,
provider retries, and enforceable token/tool limits. Until these are qualified,
OpenCode dispatch is not enabled. The earlier guessed `server/routes/session.ts`
path returned 404; the manifest retains that failed lookup alongside discovered
current handler/group paths.

## Readback projection constraints

At the same revision, the [assistant message schema](https://github.com/anomalyco/opencode/blob/57ef3828431790c53f8f333c7ffbfe88770a1812/packages/schema/src/v1/session.ts)
binds session/message identity, parent message, provider/model, agent, working
directories, optional variant, timestamps, error and finish reason. The schema's
cost and token fields accept finite numbers; cost may be fractional. The external
wire decoder therefore cannot apply EngOrch's integer-only canonical domain to
the entire raw response. It must reject duplicate/ambiguous keys, project supported
fields, validate exact integral token counts, and encode the internal projection
canonically. Raw fractional cost must not silently become integer micro-USD or
subscription execution cost.

The prompt implementation sets both a completion timestamp and `tool-calls` on
intermediate assistant messages. Its loop excludes `tool-calls` and `unknown` from
the ordinary finished branch. Thus a timestamp alone is insufficient evidence of
a completed inference. The adapter must bind parent/session and requested routing,
exclude summary/compaction output and incomplete tool steps, and admit only an
explicitly supported final state. A length-limited response must not be promoted
to successful complete output just because the upstream loop stopped.

The schema and core re-export were retained and hashed in the local manifest.
These are source-level findings, not executed OpenCode protocol qualification.

## Session creation effects

The pinned [session creation schema](https://github.com/anomalyco/opencode/blob/57ef3828431790c53f8f333c7ffbfe88770a1812/packages/opencode/src/session/session.ts)
accepts title, metadata, agent/model, permissions, parent and workspace. It does not
accept a caller-selected session ID. Therefore loss of the creation response must
not lead to blind recreation. A future adapter needs a durable unique marker,
bounded list/readback and ambiguity rejection before binding the server-generated
session ID. Metadata shape and listing guarantees still require inspection.

The HTTP handler delegates creation to the
[session sharing wrapper](https://github.com/anomalyco/opencode/blob/57ef3828431790c53f8f333c7ffbfe88770a1812/packages/opencode/src/share/session.ts).
That wrapper can asynchronously share a newly created root session when an
auto-share flag or configuration setting is enabled. Host admission must explicitly
disable sharing and eliminate ambient flag/config sources before session creation.
Ordinary local-server access alone does not establish a local-only execution
boundary. No session creation has been sent by EngOrch during this inspection.

Both implementation files were retained with hashes. Earlier guessed index/service
paths returned 404 and are retained as failed lookups; imports identified the
actual `session/session.ts` and `share/session.ts` paths.

## Host configuration ordering

The pinned [configuration loader](https://github.com/anomalyco/opencode/blob/57ef3828431790c53f8f333c7ffbfe88770a1812/packages/opencode/src/config/config.ts)
reads authentication state first. Well-known auth entries can trigger remote
configuration fetching. It then merges global and selected file configuration,
optionally discovers project configuration, scans configuration directories for
agents/commands/plugins and finally merges inline configuration. Therefore empty
inline plugin configuration cannot be assumed to undo earlier discovery or effects.
The host must use private auth/config directories, admit only the selected auth
entry, and disable project discovery before process initialization.

The [runtime flags](https://github.com/anomalyco/opencode/blob/57ef3828431790c53f8f333c7ffbfe88770a1812/packages/opencode/src/effect/runtime-flags.ts)
include auto-share, default-plugin, external-skill and LSP-download controls plus
an experimental output-token maximum. This inventory alone does not prove those
controls cover every path. Directory discovery, core flag precedence and actual
output-limit enforcement still require inspection and a real isolated-host probe.
Selected files and hashes are retained in the local manifest. No executable was
installed or launched, and no authentication data was read in this investigation.

## Release executable probe

The later local probe used official Windows x64 release `1.18.29`, tag
`16747470f976aca3d362ad730bcd3fe82ecc2c9a`. All fourteen selected source files
matched the previously inspected revision byte-for-byte. This is a selected-file
comparison, not a whole-build attestation. Archive SHA-256 matched the release
asset digest; executable identity and download metadata are retained locally.

An opt-in Go test started the pinned executable with `HostEnvironment`, private
home/XDG/temp directories and fixture server credentials. GET `/config` reported
`share: disabled`, no configured plugins and wildcard permission `deny`. The process
was terminated and waited on by test cleanup. No session or model was created.
This proves configuration readback for this executable on this Windows host; it
does not prove absence of every plugin code path, network effect or sandbox escape.

## Session and message persistence probes

The subsequent opt-in probe created a session on the same isolated release host,
independently read its identity and denied permissions, then recovered the same
session through the exact creation marker. It also saved one text user message
with `noReply: true` and read back its exact message/session identity, agent,
provider, model, variant and text. The complete OpenCode package passed with
these executable probes enabled on Windows. No model inference was performed;
the fixture provider/model names establish persistence shape only.

One earlier host start exceeded the 30-second readiness deadline. Later probes
passed, but the timeout cause remains unknown. The probe now captures bounded
process diagnostics and observes early exit; these changes do not establish
that the underlying startup issue is fixed.

`PromptOnce` implements a single asynchronous POST. HTTP 204 means acceptance
only. `ReadPrompt` performs a read-only exact-message check; absence or failure
does not authorize a repeated POST. Dispatch error behavior is covered with
HTTP fixtures. Durable controller admission, completion recovery, provider
accounting and actual model-turn qualification remain incomplete.

## Completion recovery cannot rely on idle status alone

The release-pinned [session status implementation](https://github.com/anomalyco/opencode/blob/16747470f976aca3d362ad730bcd3fe82ecc2c9a/packages/opencode/src/session/status.ts)
stores activity in an instance-local in-memory map. Setting idle removes the
entry; querying an absent entry returns idle. The HTTP status endpoint returns
the map. Therefore absence from `/session/status` is not durable completion
evidence, particularly after host restart. It cannot authorize resubmission or
release the invocation budget as completed.

Completion admission must combine durable prompt binding, persisted assistant
finish and output validation, bounded complete message-history observations,
and the controller's owned-host lifecycle. A current idle observation may be
an additional check on that host; it cannot replace those requirements. Existing
`ReadMessage` explicitly validates one message only, and `RecoverPrompt` proves
user-message persistence only. Neither currently establishes whole-turn closure.

## Intermittent instance initialization investigation

An instrumented three-run Windows probe passed twice and timed out once at
`/config` after 30 seconds. A separate two-second `/global/health` request on
the still-owned failed host returned `healthy: true`, version `1.18.29`. The
failure is therefore beyond the listening socket and global health handler;
the instance initialization path remains under investigation.

The pinned configuration loader starts dependency installation in detached
fibers. The [npm service](https://github.com/anomalyco/opencode/blob/16747470f976aca3d362ad730bcd3fe82ecc2c9a/packages/core/src/npm.ts)
can run Arborist reification when node_modules is absent or package declarations
are not covered by the lockfile, with install scripts disabled. Empty configured
plugins alone do not suppress this background install. However,
[plugin initialization](https://github.com/anomalyco/opencode/blob/16747470f976aca3d362ad730bcd3fe82ecc2c9a/packages/opencode/src/plugin/index.ts)
awaits those dependencies only when external plugin origins are nonempty.
Consequently a dependency wait is not yet a demonstrated explanation for the
observed empty-plugin configuration timeout. Do not report the npm operation
as the root cause or treat a larger readiness timeout as a fix.

## Output limit scope

The release-pinned [request preparation](https://github.com/anomalyco/opencode/blob/16747470f976aca3d362ad730bcd3fe82ecc2c9a/packages/opencode/src/session/llm/request.ts)
passes the runtime output-token flag through the model limit calculation, then
through the `chat.params` plugin hook. The resulting maxOutputTokens is passed
to generation. `StartLimitedProcess` now supplies an explicit positive flag in
the scrubbed launch environment. This establishes configuration plumbing only.
It does not prove provider enforcement, bound input/system/tool-schema tokens,
or cap the cumulative cost of multiple generation steps. Provider wire tests
and whole-invocation accounting remain required before claiming budget control.
