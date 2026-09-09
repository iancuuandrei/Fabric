# OpenCode schema projection

The provider schema projection in `internal/opencode/provider_schema.go` adapts
the recursive OpenAI schema sanitizer from OpenCode v1.18.29, revision
`16747470f976aca3d362ad730bcd3fe82ecc2c9a`.

Upstream source: `packages/opencode/src/provider/transform.ts`, lines 1472–1560.
Full source file SHA-256:
`01b2442770a25f943fd7884ff3f3dac838344a568690e395a77b7bd17c8b0ab5`.

The Go adaptation makes the pinned runtime's outgoing tool schema transformation
explicit and independently testable. It binds the original and projected schemas
separately. The context broker continues to enforce the original argument
constraints, including numeric limits removed by the upstream wire projection.
This is not a vendored OpenCode runtime or a grant of tool authority.

The unmodified upstream MIT license is retained as `LICENSE`.
Copyright (c) 2025 opencode. License SHA-256:
`625f0f619133f89bbbb2abe37369613dfa1885eba1e50d02170deb62bb42cb6b`.
