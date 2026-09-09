# Incomplete Responses terminals and success trailers

M2h's sixth HTTP 200 response ended with `response.incomplete`, reason `max_output_tokens`. Exact raw evidence showed 16,384 output tokens, including 10,054 reasoning tokens. The provider did not deliver a completed proposal. The prior mandatory cost-ping precheck obscured this failure with a secondary missing-trailer error.

The parser must report a validated incomplete/failed terminal as failure without requiring a success-only trailing cost ping. This does not authorize partial proposals, usage settlement as success, retries, or inferred completion. A completed response still requires the configured trailing ping. Malformed, duplicate, pre-terminal, or uncontracted pings remain rejected.

The bootstrap route's numeric output limit is separate from unlimited EngOrch accounting. For the next qualification, the configuration uses the OpenCode native catalog's advertised 131,072 output and 1,048,576 context limits. The captured catalog source explicitly notes that 1.3 metadata follows 1.2 pending public 1.3 specifications; these are advertised limits, not a live-proven maximum. Exact source and native catalog hashes are retained in the runner's private qualification evidence.

No provider/model, task requirements, writable paths, verification, or stock runtime changes are part of this correction. M2h remains NO-GO and its invocation is never replayed.
