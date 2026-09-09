// Package repository owns read-only Git identity observations, not checkout or
// effect authority. See docs/specifications/repository-identity.md. Later writer
// admission must repeat observations; discovery does not lock the repository.
package repository
