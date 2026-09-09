// Package control owns the finite run state machine and semantic journal replay.
// It authorizes transitions, not model reasoning. It may depend on canonical,
// repository, runtime and journal contracts; those packages must not depend on it.
// See docs/specifications/state-machine.md and docs/adr/0002-authority.md.
package control
