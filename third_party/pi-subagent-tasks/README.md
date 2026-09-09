# pi-subagent-tasks concurrency adaptation

Source: harms-haus/pi-subagent-tasks, MIT, exact commit
`2bae805c1a0bdd97e699e2c9601fb4e6624f6f53`.

`internal/taskpool/pool.go` adapts the all-applicable capacity check followed by
atomic acquisition from `src/pools.ts`, `createPoolCoordinator`. Original file
SHA-256: `3faa83cf7cb06f3dd423b1ef2996b1c029a471fe4b77742246e3c3965845df73`.
The retained LICENSE is copied without modification from that checkout.

`internal/taskpool/dag.go` also adapts the exact-ID dependency validation and
unique-downstream priority pipeline from `src/dag.ts` at the same revision.
Original SHA-256: `cc43d50d9c28a192693f0704d4e26c238af88a8cf85ea4ae4802647ee7129785`.
Differences: no automatic IDs or title lookup, duplicate dependencies reject,
iterative topological validation replaces recursive DFS, and bounded bitsets
compute unique downstream counts. Stable declaration order breaks priority ties.

Changes: Go implementation; durable canonical journal events and SQLite
transactions replace process-local counters; access-profile capacity is added;
provider/model identity uses a tuple rather than slash concatenation; exact
attempt IDs cannot be reused; release requires a live reservation and a receipt
digest rather than silently clamping counters at zero. Missing terminal evidence
retains capacity. The controller must validate evidence and external authority.

Tests and benchmarks are EngOrch-specific adaptations of the capacity properties.
The original donor test suite was not executed because its sibling file dependency
was unavailable. EngOrch's focused ordinary and race tests passed. Controller
dispatch integration and durable-recovery stress qualification remain outstanding.
