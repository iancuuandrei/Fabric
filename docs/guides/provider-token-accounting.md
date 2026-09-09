# Harness accounting and provider technical limits

`unlimited_tokens = true` with `tokens = 0` disables the EngOrch token
consumption ceiling for that run or role. Observed input/output usage remains
recorded. Cost, concurrency, exact identity, terminal settlement and technical
route constraints are not disabled.

For an unlimited provider-backed invocation, gateway evidence records:

- `engorch_budget.mode = "unlimited"` and legacy `reserved_tokens = 0`;
- `provider_reservation.tokens`, derived from the validated model contract;
- `provider_reservation.reason = "model_contract_conservative_maximum"`;
- `provider_reservation.hard_limit = true`.

The technical bound is the existing explicit model-contract calculation:
`max_calls * (context_window_tokens + max_output_tokens)`. It is not an
invented admission budget or an observation of a provider's advertised maximum.
These route fields are operator declarations and remain bound to model/config
identity. Missing or invalid bounds fail configuration/admission. No default
numeric reservation is invented for unlimited accounting.

Per-call output, total response, request/response byte, call count, and pricing
constraints remain enforced. An observation exceeding a hard technical contract
is rejected as a provider contract violation, not budget exhaustion. There is no
soft reservation mode in this bounded implementation. Unlimited accounting alone
can accept arbitrary representable observed totals; it does not override a hard
technical contract. Reasoning and cache detail remain subsets, never additional
tokens.

Historical finite bindings retain their serialized identity and finite budget
semantics. The new explicit fields are absent on those bindings. Access intent,
gateway binding, request/response receipts and terminal access receipt remain
linked by exact hashes; the numeric provider reservation is never copied into an
unlimited access reservation.
