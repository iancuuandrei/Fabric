# OpenCode message readback

`internal/opencode` currently implements exact-message observation, not a complete
AgentRuntime. Session provisioning, submission, whole-turn discovery, cancellation,
credential-host admission and controller dispatch remain unimplemented.

The client accepts only explicit HTTP loopback IP addresses with a port and Basic
server credentials. DNS names, URL userinfo, path prefixes, proxies and redirects
are rejected or disabled. This restriction is not OS sandboxing or proof of server
identity. The controller must separately establish ownership of the local server.
The client uses an owned transport, a 30-second request deadline, 64-KiB response
header limit and 1-MiB body limit. Errors do not expose response bodies or credentials.

`ReadMessage` issues GET for one exact session/message locator. The returned
assistant ID must match the requested ID, not merely be internally consistent.
Metadata binds parent message, provider/model, agent, workspace and optional
variant. Only `stop` with an ordered completion timestamp is accepted. Summaries,
errors, intermediate tool calls and truncated completion reasons are rejected.

The wire decoder retains original numeric lexemes while rejecting duplicate keys,
invalid Unicode, excessive nesting and nonfinite numeric ranges. Projected token
counts must be nonnegative safe integers. Fractional upstream cost is not converted
into execution cost. Visible text preserves part order, with unique IDs and exact
session/message binding. Output is limited to 256 KiB and 4096 parts. Reasoning is
not visible output. Unsupported tool/file/compaction parts and explicitly excluded
or unfinished text cause rejection.

Executed local HTTP fixtures cover successful metadata/text projection, a fully
consistent response for the wrong message, truncated Content-Length, unsupported
media type, oversized body, duplicate metadata, redirects and HTTP errors. The
package race test passed on Windows. These fixtures do not prove compatibility
with a running OpenCode server or finality of an entire session.

Protocol inspection and pinned upstream evidence are recorded in the
[OpenCode research note](../research/opencode-access.md).

An opt-in Windows probe also runs the pinned OpenCode 1.18.29 executable in a
temporary private environment. A project configuration attempts to enable sharing,
allow permissions and load a nonexistent local plugin. The actual server's
authenticated configuration readback still reports sharing disabled, no plugins
and wildcard permission denial; an unauthenticated request returns HTTP 401.
The probe creates no sessions or model calls and terminates and waits for its
child process. This checks these specific configuration and authentication
boundaries, not OS sandboxing, inference compatibility or session finality.
