// Package codexusage accumulates cumulative token-usage observations emitted by
// Codex app-server. It is deterministic and has no persistence or runtime side
// effects. Callers must durably retain a raw notification before observing it.
package codexusage
