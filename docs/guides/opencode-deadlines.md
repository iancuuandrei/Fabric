# OpenCode runtime deadlines and lost completion

A synchronous OpenCode message POST can remain open for the entire model turn.
It is bounded by the invocation context, not by the ordinary HTTP readback
budget. Connection establishment remains bounded. Ordinary metadata/readback
operations retain their own finite total and response-header bounds. A response
header timeout on the long POST must not cut off legitimate inference while
OpenCode is still producing its terminal response.

The existing repository-owned configuration supports optional fields:

```toml
[opencode]
# existing pinned executable/state fields also required
invocation_timeout_seconds = 900
readback_timeout_seconds = 30
```

Zero/omitted values preserve the defaults and historical JSON identities.
Invocation duration is limited to 7200 seconds and readback to 300 seconds.
An earlier caller deadline is never extended. The separate provider request
limit remains five minutes in R13; increasing the outer invocation budget does
not remove that limit or grant another provider call. Model/provider fallback
and retry admission are unchanged.

Transport failure and provider outcome are different evidence. A request that
may have been dispatched has UNKNOWN local completion until the exact original
session and terminal result are reconciled. A network timeout never authorizes
resending the prompt. The public error remains stable; the typed diagnostic
retains bounded transport/context errors separately, operation phase, elapsed
time, endpoint and known request/session identifiers without credentials.

R13 keeps the durable original session/dispatch identity. Bounded read-only
recovery is permitted only through the existing exact-identity single-tool
readback and sealing path while the same owned host is available. It does not
restart a host or model, and it does not accept arbitrary recovered text as a
terminal receipt. Unsupported or contradictory recovery remains UNKNOWN.
Composite dispatch recovery is still journal-only; this change does not add a
new asynchronous dispatch protocol or replay authority.

M1c remains quarantined. Its provider completion receipt is preserved, but it
has no native terminal settlement. The offline slow-server fixture establishes
the old HTTP timeout mechanism; it does not retroactively prove the exact lost
transport error from M1c.
