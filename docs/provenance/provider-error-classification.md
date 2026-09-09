# Provider error classification

Inspected on 2026-09-08. The implementation is original Go code; no source code
was copied. These references inform a finite diagnostic classifier, not retry
policy, usage settlement or provider cancellation authority.

Anthropic documents status classes for authentication, permissions, invalid
requests, rate limits, timeouts and server overload. Its documented 400 and 429
classes can also cover spend limits, so neither status alone identifies the
underlying budget condition. The classifier does not search human-readable
messages to invent that distinction. Billing status 402 remains unknown rather
than being relabelled as a proven exhausted quota.
[Claude API errors](https://platform.claude.com/docs/en/api/errors).

OpenAI documents that a billing-related error can retain the broader
`insufficient_quota` type. The bounded Chat/Responses error object may refine a
429 to QUOTA when that exact machine code or type is present. Unrecognized,
oversize or duplicate-key objects cannot supply that refinement.
[OpenAI usage and spend limits](https://help.openai.com/en/articles/6614457).

Only finite categories, phase, HTTP status and optional response digest/size
reach the diagnostic. Provider bodies and messages are not retained or returned
through the error. A class does not mean a provider-confirmed cancellation,
settled usage, retry authorization or released reservation. The transport uses
one attempt; SDK retry recommendations from upstream documentation do not alter
this contract. See the [conformance contract](../specifications/model-adapter-conformance.md)
and [execution status](../evaluation/status.md) for implemented and tested scope.
