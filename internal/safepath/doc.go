// Package safepath owns portable writer path validation and alias rejection.
// It does not authorize a mutation or provide an OS sandbox. Callers must use
// os.Root for actual access and hold their writer lease. See
// docs/specifications/worktree.md for limits and trust assumptions.
package safepath
