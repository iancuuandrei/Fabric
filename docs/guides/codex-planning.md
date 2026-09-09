# Plan with the Codex runtime

Scope: authenticated planning with durable runtime evidence. The planner currently
receives the objective text. Repository reading tools and the broader engineering
workflow are still under development; do not treat this as whole-repository
analysis or a production isolation qualification.

Start with `harness init` in a committed Git repository. Edit the `[planner]`
section in `harness.toml` to select `runtime = "codex-app-server"`,
`provider = "openai"`, an explicit model and effort, and `role = "planner"`.
Keep the required verification checks appropriate to that repository.

Add a `[codex]` table with these four string fields:

| Field | Required value |
| --- | --- |
| executable | Absolute path to the installed Codex executable |
| executable_hash | Lowercase SHA-256 of that exact executable |
| state_root | Existing private directory outside the source repository |
| auth_source | Absolute path to an existing local ChatGPT auth.json file |

On PowerShell, obtain the binary path and digest without inspecting credentials:

```powershell
(Get-Command codex).Source
(Get-FileHash -Algorithm SHA256 -LiteralPath (Get-Command codex).Source).Hash.ToLowerInvariant()
```

Use forward slashes in TOML paths, or TOML literal strings, to avoid accidental
backslash escapes. Keep authentication contents out of configuration and commits.
The host supplies the existing access token over local stdio; it neither copies
the authentication file nor sends refresh/ID tokens. Token refresh is not yet
implemented. Replacing the executable requires explicitly rebinding its hash.

```text
harness doctor
harness plan "Describe the proposed engineering change and its required checks."
```

The resulting run enters AWAITING_APPROVAL only after matching host and runtime
receipts are recorded. Inspection exposes the host identity, thread/turn handles,
runtime journal head and result hash. `harness resume RUN` returns an already
completed plan or reads the exact recorded runtime turn. It never resends a turn
whose creation outcome is unknown.

The planner can discover committed paths with `source_list` and read their exact
bytes with `source_read`. Text fragments also include `content_utf8` when valid.
Both tools use the run's fixed commit, so unsaved or uncommitted source edits are
not part of this view. Their requests and responses are retained in planner.jsonl
alongside provider thread and turn evidence. This does not yet automate the
implementation, repair or review phases.

Each run gets a separate directory below state_root. A complete prepared host can
be observed after interruption; partial preparation or stale execution markers
require inspection. Do not delete markers while a process remains active. The
runtime's explicit empty environments prevent granting native environment access
through these requests; see [host admission](../specifications/codex-host.md) for
the evidence and remaining qualification limits.
