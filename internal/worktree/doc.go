// Package worktree owns isolated Git workspace creation, admission, candidate
// fingerprints and exclusive writer leases. The controller must persist intent
// before calling Create and receipts after observing results. This package does
// not infer approval or retry failed creation. See docs/specifications/worktree.md.
package worktree
