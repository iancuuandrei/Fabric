# Codex capability confinement

Requested configuration is not evidence of the effective runtime tool surface.
In particular, disabled collaboration feature flags do not enumerate the native
tools selected by model metadata. A dynamic-tool list describes only the tools
supplied by EngOrch, and an empty MCP inventory describes only MCP tools.

For strict runs using a version 2 configuration, request:

```toml
[codex]
# Keep the existing executable, identity, state and auth settings.
capability_confinement = "required"
```

The controller applies this requirement to Codex planner, explorer, writer,
fixer and reviewer execution through the shared host admission path. It applies
before controller login, model access admission, thread creation, resumed
execution and turn dispatch. Ordinary configuration admission still runs first.

Positive R17 admission additionally requires an absolute controller-owned
`codex.capability_attestation_path` and its exact
`codex.capability_attestation_sha256`. These immutable configuration values are
not model-selected authority. Missing, mismatched, expired or unqualified proof
returns `CAPABILITY_CONFINEMENT_UNVERIFIED`. Absence of the new settings retains
legacy behavior without qualifying historical routes as confined.

The requested process and fresh-thread controls additionally set
`agents.enabled=false`, with both `multi_agent` and `multi_agent_v2` disabled.
These controls defend against model-selected native child execution. They do
not substitute for the strict gate. Historical thread configuration and route
receipts are not rewritten; denied resumption must not retry an old request.

R17 qualifies the effective registry using the exact installed binary and
byte-pinned Sol metadata. An explicitly documented loopback provider substitution
captures actual request bytes without credentials or upstream inference.
Responses Lite inventories include `input[type=additional_tools].tools`.
The fixed ten-case proof covers initial advertisement, forced plain/namespaced
spawn with absent or faulty hooks, successful source-tool execution followed by
rejected spawn in the same turn for planner and reviewer catalogs, and deferred
search. Every case has one root thread and no child rollout or usage event.

Native `tool_search_call` remains executable even when unadvertised, but returns
an empty tool list for collaboration. This is evidence specifically about native
collaboration effects, not every unadvertised capability. Handler non-entry is
inferred from registry rejection and source control flow, not stack telemetry.
Hooks have no authorization role: a valid deny blocks, but exit-code-1 failure
permits execution when spawn is otherwise enabled.

The gate hashes proof files and derives facts from raw requests, RPC and rollout
records. It checks binary and selected metadata identities, complete normalized
CLI controls, and exact role/tool profiles. Distinct override ordering is
immaterial; additional or duplicate controls are rejected. The faulty-hook cases
are an explicit diagnostic delta. The manifest covers exactly the qualified
planner and reviewer profiles and expires within 24 hours.

Before authentication, the host copies the verified model catalog into its
isolated home and pins it using `model_catalog_json`. It checks initialized
server identity, runs the existing host audit, revalidates proof, and binds the
connection to one profile, workspace and tool catalog. Typed lifecycle calls
enforce this binding and expiry. Unapproved raw RPC effects are rejected, and
foreign-child detection remains fail closed.

Host receipts bind the manifest SHA to launch identity. Replay compares it with
the immutable creation configuration and rejects removal or substitution without
rereading historical mutable files. New receipt/thread fields are omitted for
legacy runs; resumed configuration never rewrites historical dispatch evidence.

R15/R16 evidence and failed M2 attempts remain historical artifacts. M1 read-only
Muse qualification is unchanged. Registry qualification is offline evidence;
authenticated execution, model quality and the next end-to-end M2 result are
reported separately.

The standalone `d0-transport-probe` diagnostic also requires strict confinement
unconditionally. It cannot be used to bypass the controller's gate and issue a
synthetic Sol invocation while the effective surface is unverified.
