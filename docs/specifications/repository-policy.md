# Repository policy/config v1

The operator explicitly selects `harness.toml`. Parsing MUST reject unknown keys,
duplicates, oversized input (64 KiB), unsupported versions and invalid profiles.
The entire semantic configuration is bound into run creation. Formatting does
not affect its canonical identity. Required verification cannot be removed by
runtime output or RI. One to 64 uniquely named checks are required; each declares
direct argv and a timeout of 1–86400 seconds. No implicit shell interpolation.

The initial schema contains version, repository logical name, base branch,
planner profile and verification entries. Later policy additions require schema
review. Config declares checks; it is not evidence they ran. The fake example
declares Go tests; operators of other languages must change it before planning.
Existing runs keep their bound configuration rather than loading changed policy.

Configuration is an operator input, not an authority grant from repository text.
The initial kernel performs no writer or external effect. Future admission must
bind separate exact effect approval and path/command constraints. Configuration
alone cannot make those unimplemented operations available.

See the executable example in `internal/config/config.go`, validated by tests.
