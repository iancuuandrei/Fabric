# pi-multiagents-v2 adaptation provenance

EngOrch's `internal/agenttree` package adapts bounded topology, path
reservation, lifecycle, and mailbox mechanisms from
`YoungseokCh/pi-multiagents-v2` at revision
`fb056dd9ea65f32ee8e8da7b57e457d93bdd6ccd`.

Inspected upstream files:

- `extensions/core.ts`, SHA-256
  `a50a4c31196c3bb15a677dfb91fbe1840938fb947c4fd6bb55d7070630cea342`
- `extensions/team-manager.ts`, SHA-256
  `6340f98b61e3ef072e659256ba46f93aa6ac99beb409b65ddefcd2bd62b2b669`
- `LICENSE`, SHA-256
  `706be33671487656a92f9841f7f902c6e4df2c5257f0bcedd0ed6f1b3ed441b6`

The implementation is an adaptation rather than a verbatim copy. It replaces
in-memory TypeScript session state with bounded, replay-validated Go journal
events; assigns opaque content identities separately from display paths; uses
two-phase durable child reservation; binds immutable context digests and access
authority; and records monotonic digest-only mailbox envelopes. Runtime and
capacity ownership remain in EngOrch's controller and TaskPool.

The upstream MIT license is retained byte-for-byte in `LICENSE`.
