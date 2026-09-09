// Package fileeffects validates and applies exact regular-file proposals inside
// a bound worktree. The controller owns approval, journal intent and receipts;
// this adapter owns prediction and rooted filesystem mechanics. See
// docs/specifications/file-effects.md for bounds and uncertainty semantics.
package fileeffects
