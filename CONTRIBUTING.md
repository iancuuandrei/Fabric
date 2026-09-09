# Contributing

Development is local while the initial v1.0.0 change-set is built. Do not publish
or modify LexAI. Use Go 1.27.1; Rust tooling is pinned for the later RI phase.

Run formatting, tests and static analysis:

```sh
go fmt ./...
```

```sh
go test ./...
```

```sh
go vet ./...
```

```sh
go test -race ./...
```

The race detector needs a supported C toolchain. Report unavailable checks as
NOT_RUN. Test output is evidence of the executed scope, never provider or hosted
qualification. Add fault cases for durable state and authority boundaries.

Follow the [documentation standard](docs/contributing/documentation-standard.md).
Use ADRs for consequential decisions and update specifications with behavior.
Keep changes reviewable within the single initial PR. Before calling it complete,
challenge ownership, unnecessary abstractions, duplicated authority, explicit
errors, reproducible failure, uncertainty and performance regressions.

Regenerate [CLI reference](docs/reference/cli.md) from `harness reference`; preserve
UTF-8 and LF. Test fixtures create temporary independent repositories and never
change the developer's checkout. Security limitations are documented in the
[trust boundary](docs/security/trust-boundary.md).
