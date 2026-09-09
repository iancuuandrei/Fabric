# Vercel AI SDK test payloads

`internal/providergateway/anthropic_sse_test.go` retains four recorded event-payload
fixtures from `vercel/ai`, revision
`85464f4e2026d9fc0274424c0171a25742836411`, corresponding to
`@ai-sdk/anthropic@3.0.111`.

Upstream paths under `packages/anthropic/src/__fixtures__/`:

| File | Original payload bytes SHA-256 |
| --- | --- |
| `anthropic-text.chunks.txt` | `12798adc987ad4bed12408a64c37f9816be3182ebe48c7355f0bf36b29f40095` |
| `anthropic-json-tool.1.chunks.txt` | `c8a4f791c08ca46d400b1d1c6b555d687bd8d4ce40f547fc68c531a95df16c9b` |
| `anthropic-refusal.chunks.txt` | `e9d5d6cf72a0c8c067d044086a040d0fc1572459df89557595c3b1cb223fe06d` |
| `anthropic-message-delta-input-tokens.chunks.txt` | `98c66d799cfb6cc758be6c99928154d9e181ab4d4df9fe5e98ab64e07fd6eb8c` |

Modification: tests frame each recorded JSON payload with the matching named SSE
event from its `type`. The source payloads are retained; the SSE framing is
constructed by these tests and is not a captured production response.

Copyright 2023 Vercel, Inc. The exact upstream license notice is retained in
`LICENSE` (SHA-256
`b4f9adb7c568904834d0dd6cc98d16c390d21ca32fc17ae7a267715269bd5529`).
`LICENSE-APACHE-2.0` supplies the full Apache License, Version 2.0 text.
No root `NOTICE` file was present at the inspected revision.
