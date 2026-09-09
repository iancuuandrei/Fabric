// Package codexrpc implements bounded, serial Codex App Server stdio envelopes.
// It grants no tool permissions, retries no requests and closes uncertain streams
// on cancellation. It is transport infrastructure, not an AgentRuntime adapter.
package codexrpc
