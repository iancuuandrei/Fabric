# Classic Controller, Provider & RI Internals

Fabric v2 preserves the full deterministic v1 state-machine controller (`fabric run --autonomous`), content-addressed journal replay, and Rust `fabric-ri` semantic engine. This index groups those deep-dive reference guides so the main [Documentation Hub](../README.md) stays concise.

## Workflow & Isolation Architecture
- [Source-build autonomous task guide](autonomous-task.md)
- [Autonomous task graphs](graph-autonomous-task.md)
- [Parallel and isolated writers](isolated-writers.md)
- [Impact-aware review](reviewer-impact-context.md)
- [Checkpoints and continuation](checkpoints.md) · [Verified fresh rounds](verified-fresh-rounds.md)
- [Repair diagnosis](repair-diagnosis.md) · [Coverage localization](repair-localization.md)
- [Task schedules](task-schedules.md) · [File changes](file-changes.md)

## Context, Planning & Repository Intelligence (Rust `fabric-ri`)
- [Engineering orientation & semantic queries](engineering-orientation.md)
- [Go file facts](go-file-facts.md) · [Go context Personalized PageRank](go-context-ppr.md)
- [Context and planner contracts](planner-contract.md) · [Context selection](context-selection.md)
- [Agent context resolution](agent-context.md) · [Engineering skills](procedures.md)
- [Editable working context](working-context.md) · [Evidence value inspection](evidence-value.md)
- [Native compaction](native-auto-compaction.md) · [Prompt-cache recipes](prompt-cache-recipes.md)
- [Deterministic Go formatting observation](deterministic-go-format-observation.md)

## Runtimes, Providers & Accounting
- [Capabilities and feature matrix](features.md)
- [Runtime capability limits](graceful-capabilities.md) · [Codex capability confinement](codex-capability-confinement.md)
- [Codex planning](codex-planning.md) · [Codex native usage observations](codex-native-usage-observations.md)
- [Model routing](model-routing.md) · [Model allocation](model-allocation.md) · [Empirical model calibration](empirical-model-calibration.md)
- [OpenCode deadlines](opencode-deadlines.md) · [OpenCode native writer output](opencode-native-writer-output.md) · [OpenCode text phase metadata](opencode-text-phase-metadata.md)
- [Provider item identity](provider-item-identity.md) · [Provider request rejection evidence](provider-request-rejection-evidence.md)
- [Provider response evidence](provider-response-evidence.md) · [Provider token accounting](provider-token-accounting.md)
- [Responses continuation replay](responses-continuation-replay.md) · [Responses incomplete terminal](responses-incomplete-terminal.md) · [Responses message phase](responses-message-phase.md)
- [Bootstrap topology confinement](bootstrap-topology-confinement.md) · [Controller state](controller-state.md)
- [Muse Go route](muse-go-route.md) · [Local packaging](local-packaging.md) · [Release guide](release.md)
