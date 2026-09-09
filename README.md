# EngOrch — v0.0.0 experimental source checkpoint

A standalone engineering harness around coding agents: models supply judgment;
deterministic code binds repository context, routing, approvals and outcomes.
Go owns orchestration. Rust owns immutable repository intelligence.

The local implementation includes deterministic planning and effect journals,
isolated writer workspaces, approved file changes, verification execution, a Codex
planning/source-tool adapter and Rust SCIP intelligence. Explicit producer, import
and local publication lifecycles feed immutable runtime queries. Seven optional
engineering procedures accompany the code.

This is a public review snapshot, not a trusted bootstrap release. The latest
heterogeneous M2 attempt remains **NO-GO**: Muse returned a settled structured
proposal, but controller replay rejected the order of its file changes. The R33
fix is qualified offline; no subsequent live attempt has run. Historical evidence
is described in [the v0.0.0 qualification report](docs/evaluation/v0.0.0.md).

The qualification runtime depends on two unupstreamed OpenCode patches. Stock
OpenCode equivalence, full-product readiness and OS sandboxing are not claimed.
No binaries, credentials, raw provider responses or private runtime state are
distributed in this repository.

## Try a local plan

Build from source with Go 1.27.1 and Git installed:

```sh
go build -o bin/harness ./cmd/harness
```

Use a repository with an existing commit. `init` writes `harness.toml` without
overwriting an existing file. Edit its repository name and required checks.

```sh
harness --root PATH_TO_REPOSITORY init
```

```sh
harness --root PATH_TO_REPOSITORY plan "Add a tested greeting"
```

For an existing goal document, use
`harness --root PATH_TO_REPOSITORY plan --file goal.md`. Relative goal paths resolve
against the selected repository. The file must contain nonempty UTF-8 text of at
most 256 KiB; its exact text is retained in the run's immutable inputs.

The result is canonical JSON in `AWAITING_APPROVAL`, with exact run and plan IDs.
The fake plan exercises protocol mechanics; it is not an evaluated model answer.
See the [local planning guide](docs/getting-started/local-plan.md) for approval,
replay and failure behavior.

The [local Codex integration](integrations/codex/engorch/README.md) packages this
workflow as a thin skill. Codex is the intended primary interface; the same CLI
remains usable independently. Plugin installation and sandbox inheritance have
not yet been qualified.

## Architecture and development

- [Architecture](docs/architecture/system.md) and [ADRs](docs/adr/0001-language-split.md)
- [Journal contract](docs/specifications/run-journal.md)
- [CLI reference](docs/reference/cli.md)
- [Local task schedules](docs/guides/task-schedules.md)
- [Apply file changes](docs/guides/file-changes.md)
- [Optional engineering procedures](docs/guides/procedures.md)
- [Contributing](CONTRIBUTING.md) and [documentation standard](docs/contributing/documentation-standard.md)
- [Research provenance](docs/research/oss-mechanisms.md)
- [Current evidence](docs/evaluation/status.md)

LexAI is read-only design reference. This repository contains independently
implemented contracts and code, with no LexAI runtime dependency or historical
journal compatibility. See the [mechanism audit](docs/research/lexai-mechanism-audit.md).
