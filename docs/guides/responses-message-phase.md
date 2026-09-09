# Responses assistant-message phase

The M2e bootstrap qualification received a complete HTTP 200 SSE body whose assistant message included `phase: "commentary"`. The previous decoder rejected the additional field before admitting a proposal. The failed invocation remains unresolved and is never replayed or retrospectively settled by decoding its archived bytes.

The [official OpenAI response message schema](https://github.com/openai/openai-python/blob/main/src/openai/types/responses/response_output_message.py) defines optional `phase` with `commentary` and `final_answer` values, or null. It distinguishes intermediate commentary from a final answer and instructs callers to preserve the field when replaying assistant items.

Qualification must check the exact archived body offline, finite phase validation, agreement between streamed items and the terminal snapshot, unchanged legacy behavior when the field is absent, and continued rejection of unknown fields and invalid phases. A commentary message followed by a function call is a tool step, not an implementation proposal. Original response bytes remain the authority for proxy forwarding; normalized evidence must not silently relabel commentary as a final answer.

No model, runtime, output schema, writer prompt, or approved implementation scope is changed by this qualification.

The same archived body also includes `name` on `response.function_call_arguments.done`. The official OpenAI schema checked during qualification does not list this field. It is therefore an observed Muse/Zen response extension, not assumed generic protocol authority. Its explicit route capability must remain disabled by default and, when enabled, must require exact equality with the function name already bound by the output item. It never grants a tool capability.

The native OpenCode bridge compares all visible text in each tool generation against the gateway receipt. Commentary in a tool-call step must therefore remain in this text identity. A response used as the final result must not use commentary as its implementation proposal; unsupported final projections fail closed rather than changing the native response bytes or inventing final-answer evidence.

This qualification admits phase only for SSE. Non-streaming JSON retains its prior rejection of phase-bearing messages, including explicit null; shared item decoding must not silently expand JSON acceptance or discard the field.
