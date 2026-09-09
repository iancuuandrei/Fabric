# Model access revision: implementation gap

The September 7 revised objective makes access authorization independent of model,
provider and runtime. This audit records inspected gaps, not implemented behavior.
The full user request is retained locally in `.local/model-access-goal.md`.
Existing work remains local and LexAI remains read-only reference.

| Requirement | Inspected current evidence | Remaining work |
| --- | --- | --- |
| Route separates runtime/provider/model/access | `internal/runtime/runtime.go` Profile has runtime, provider, model, effort and role only | Add explicit access identity and bind it into invocation and receipt identities |
| Independent plan/explore/write/fix/review routes | `internal/config/routing.go` supports four roles; no fixer role | Add independent fixer selection and wire repair dispatch |
| API and subscription access | Controller role runners call LoginChatGPT using one Codex AuthSource | Separate access profiles and broker admission from role runners; implement API access |
| Privacy classes | Config has no repository privacy class or access class allowlist | Validate PUBLIC/PRIVATE/CONFIDENTIAL before any runtime invocation |
| Model Access Gate | Invocation validates profile identity but not access, budgets or class | Controller-owned admission and durable reservations for budget and concurrency |
| Invocation receipts | Runtime result binds requested profile and observations; unknown usage is nullable | Bind access, billing mode, privacy, attempt and admission identity; preserve unknown subscription cost |
| Two real access/runtime paths | Config admits fake and codex-app-server only | Implement and qualify a materially different real adapter and mixed step routing |
| Deterministic escalation | Route selects a role without escalation policy | Explicit permitted reason/target, renewed admission and plan approval boundaries |
| Exact reference refresh | Earlier research does not establish current remote PR state | Read-only inspection of the private design reference; private identifiers omitted |

Implementation order returns to the deterministic kernel before additional RI or
complex real-model work. First define versioned access/profile/admission contracts
and tests using the fake runtime. Then wire every planner, explorer, writer,
fixer and reviewer path through the same admission boundary, including resume.
An unbound historical invocation must not acquire new access authority on replay.
Existing journals must retain their original interpretation; schema changes need
an explicit compatibility policy rather than silent default access profiles.

Required negative proofs include disallowed provider/model, missing access,
privacy mismatch, token/cost/concurrency exhaustion, response substitution,
invocation failure and mismatched resume. Passing the existing Codex fixtures does
not prove these new requirements. Model names in the flagship routing example
are requested configuration, not verified provider availability.
