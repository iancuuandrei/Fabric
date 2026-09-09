# OpenCode Responses routes

Research was performed on September 8, 2026. It used public documentation,
public model catalogs and source at the OpenCode `1.18.29` commit
`16747470f976aca3d362ad730bcd3fe82ecc2c9a`. No credential was read and no
model request was sent. Availability below means that OpenCode currently
documents and catalogs the route. It is not an executed availability or model
quality result.

## Named goal routes

The logical provider names in `.local/model-access-goal.md` are planning names.
They are not the model selectors accepted by OpenCode. The mapping must be
explicit in a route registry so that an unknown planning name is rejected rather
than silently substituted.

| Goal route | Logical provider | OpenCode runtime selector | Endpoint | Protocol |
| --- | --- | --- | --- | --- |
| Explore | `opencode-zen` | `opencode/muse-spark-1.3-contributor-free` | `https://opencode.ai/zen/v1/responses` | OpenAI Responses SSE through `@ai-sdk/openai` |
| Write/fix | `opencode` with `opencode-go` access | `opencode-go/muse-spark-1.3-contributor` | `https://opencode.ai/zen/go/v1/responses` | OpenAI Responses SSE through `@ai-sdk/openai` |

The exact IDs and endpoints appear in the official [Zen documentation][zen]
and [Go documentation][go]. The public [Zen model list][zen-models] and
[Go model list][go-models] also returned the exact SKU IDs on the inspection
date. Their model-list objects contain only `id`, `object`, `created` and
`owned_by`; those responses do not carry request limits or privacy terms.

OpenCode `1.18.29` loads its default catalog from
`https://models.opencode.ai/api.json` in the pinned
[catalog loader][catalog-loader]. That URL and `https://models.dev/api.json`
returned the same 4,494,610 UTF-8 bytes during inspection, with SHA-256
`fe26cc25c9915d05420867ff23ef3f53958e4db35191b7abb0ade480b89aa40e`.
The two exact route entries declare:

- context limit 1,048,576 tokens and output limit 131,072 tokens;
- tool calling and reasoning support;
- text, image, video, PDF and audio input, with text output;
- a per-model provider override of `@ai-sdk/openai` even though each provider's
  default package is `@ai-sdk/openai-compatible`.

These are SKU-specific catalog declarations, so the 131,072 output bound may be
used when binding these exact catalog bytes and exact model entry. It must not be
copied to another SKU or treated as a provider-enforced reservation. The catalog
URL is mutable; a production admission needs a retained byte digest and admitted
projection rather than a fresh unbound lookup. The pinned
[provider conversion][provider-conversion] copies the catalog context/output
limits and the per-model package override into the runtime model.

## Authentication and required headers

Both route handlers at the pinned revision extract the token after the first
space in the `Authorization` header: [Zen Responses handler][zen-handler] and
[Go Responses handler][go-handler]. The pinned native OpenAI adapter's
[authentication implementation][auth-source] renders the credential as
`Authorization: Bearer <secret>`. This establishes the header form used by the
official client implementation. Credentials remain external to request bodies,
receipts and journals.

OpenCode Go additionally requires an identifying `User-Agent` and a stable
`x-opencode-session` value for each conversation, as stated in the official
[Go client requirements][go]. The gateway's pinned
[request handler][gateway-handler] reads `x-opencode-session`,
`x-opencode-request`, `x-opencode-client`, `x-opencode-project`, and
`User-Agent`; only the first and the identifying user agent are documented as
requirements for an independent Go client. A controller should derive one
bounded opaque session value from its durable conversation binding and reuse it
for every provider subcall in that conversation. It should generate a distinct
request correlation value for each subcall if that optional header is enabled.

The public Zen documentation explains how to create and connect an API key but
does not add another required request header for this route. Anonymous `public`
access observed elsewhere in OpenCode source is not evidence that the named
contributor-free route accepts unauthenticated requests.

## Request and stream contract

The pinned [native Responses protocol][responses-source] posts a JSON object to
`/v1/responses`. Its supported request projection contains:

- exact `model` and an ordered `input` list;
- optional `instructions`, function `tools`, `tool_choice`, `reasoning`, `text`,
  `prompt_cache_key`, `include`, `temperature`, `top_p` and `service_tier`;
- `store`, which can be fixed to `false` for the EngOrch route;
- the caller's bounded `max_output_tokens`;
- `stream: true`.

For the initial text-and-function subset, input items need only admit system or
user messages, assistant output text, completed `function_call` items and
matching `function_call_output` items. Function declarations contain `type:
"function"`, `name`, `description`, JSON Schema `parameters`, and an optional
`strict` flag. This is a different body from Chat Completions: there is no
`messages` array, `max_tokens`, `stream_options.include_usage`, or nested
`function` object in a tool declaration.

The [official OpenAI Responses stream reference][responses-reference] and the
pinned protocol identify the relevant lifecycle and content events. A strict
initial parser should admit only the subset it implements:

1. `response.created`, followed by `response.in_progress` when supplied;
2. `response.output_item.added` for a message, reasoning item or function call;
3. text deltas and their done/item snapshots, or function-call argument deltas
   and their done/item snapshots;
4. exactly one successful `response.completed` terminal event.

`response.incomplete`, `response.failed`, and `error` are terminal provider
outcomes, not successful call receipts. Responses streams do not use the Chat
Completions `[DONE]` marker. OpenCode's pinned recorded stream appends an
`event: ping` cost record after `response.completed`; a provider-specific parser
may admit that exact bounded extension after it has validated the terminal
response, but must reject arbitrary trailing frames.

The parser must bind one response ID and model across response snapshots, require
monotonic non-repeated sequence numbers, bind item IDs and output indexes, and
reconstruct every text or argument delta. Reconstructed values must equal the
corresponding done event, `response.output_item.done`, and terminal output item.
Function item `id` and provider `call_id` are separate identities and must stay
separate. Unknown item or hosted-tool kinds remain rejected until separately
specified.

Successful `response.completed` carries `usage.input_tokens` and
`usage.output_tokens`, which are inclusive totals. `input_tokens_details.cached_tokens`
and `output_tokens_details.reasoning_tokens` are subsets and must not be added
again. `total_tokens` must equal input plus output for the strict receipt. Missing
usage, negative or non-integral values, subsets above their parent totals, a
different response/model identity, incomplete output, or an output total above
the admitted call cap fails closed. Responses does not expose a cache-write token
field in this schema, so the internal optional cache-write category remains
unknown rather than zero.

The pinned OpenCode fixture
[native-zen-tool-loop.json][native-fixture] is useful protocol evidence, not Muse
qualification. At the pinned commit it is 21,850 UTF-8 bytes with SHA-256
`25d7b10d9639346c78b43db469e60b782efbcbcba16ad64bbbcfbac3295476a4`.
It contains two `/v1/responses` requests: a tool-call stream and a final-text
stream. Both terminate with `response.completed` usage and then the OpenCode
`ping` extension. The fixture uses another model and a proxy connection, so it
cannot prove that either Muse endpoint accepts the same complete subset.

The release package manifest pins `@ai-sdk/openai` `3.0.84`, and `bun.lock`
binds it to npm integrity
`sha512-cmgbeJL0bbY0yTJH4/AdmP5E7MjWRL9G8UdhIi0JlV/So03o82ORJofW8OzwCZPTORVQblFbpZXYGDcUd9NdUQ==`.
The exact published [3.0.84 source map][ai-sdk-map] was 527,444 UTF-8 bytes
with SHA-256
`845fef42928a43a2a77e2ad83ea4b88b63bf11260158add17d6713d6641f5efb`.
Its Responses implementation recognizes the same created, text-delta,
output-item, function-argument and terminal event families. It maps both
`response.completed` and `response.incomplete` to SDK finish output and accepts
provider additions more broadly than EngOrch's receipt contract. Consequently,
successful SDK completion alone is insufficient: EngOrch must inspect the raw
stream and admit only the bound `response.completed` projection described above.

## Commercial and privacy boundary

The Zen contributor-free SKU is listed as free for input, output and cache reads,
with no cache-write price, and is available for a limited time. Its terms permit
prompts and completions to be used to train future Meta models. Its catalog cost
fields are zero, but that is not a durable availability promise or evidence that
all effects are cost-free.

OpenCode Go is a $10/month subscription. Its published base allowance is $12 per
five hours, $30 per week and $60 per month. The Muse contributor accounting row
lists $0.10 per million input tokens, $0.20 per million output tokens and $0.002
per million cached-read tokens, with a $60 included-usage value. The estimated
request counts use an example token mix and are not request or output limits.
The Go privacy table marks the contributor SKU as usable for model training and
`Not ZDR`; it does not publish the actual retention duration. It is also limited
to supported regions.

Under a policy that disallows training or requires zero retention, neither Muse
test route is admissible for private or confidential repository content. This is
a route-policy result, independent of whether its transport is implemented.

## Generic provider architecture

Muse is a qualification route, not an architectural special case. The provider
gateway should select from a closed, versioned protocol registry:

| Protocol contract | Endpoint suffix | Request/stream implementation |
| --- | --- | --- |
| `openai-chat-completions-sse-v1` | `/v1/chat/completions` | Existing strict Chat request and SSE parsers |
| `openai-responses-sse-v1` | `/v1/responses` | New Responses input and lifecycle parsers described above |
| `anthropic-messages-sse-v1` | `/v1/messages` | Separate Messages request and event parser before admission |

Each route record should bind the logical provider name, runtime provider ID,
exact model ID, endpoint, protocol ID, credential mode, required non-secret
header names, capability projection, limits, privacy class, pricing mode, and
catalog/document receipt. Parser selection follows the bound protocol ID, not a
model-name prefix or provider-wide default. Authentication follows the route's
closed credential mode: for example bearer authorization for these Responses
routes, while an Anthropic route can declare its separately researched key and
version headers. The registry should not accept arbitrary header or environment
maps.

The current Chat-only endpoint and model contracts cannot truthfully bind these
Responses routes. Generalizing their enumerated protocol and path validation is
required before adding a Responses request validator and observation adapter.
The journal's inclusive usage structure already matches the Responses accounting
categories; cache write remains optional/unknown.

## Smallest fixture pipeline

The next local qualification needs no provider credential or model inference:

1. Add a strict Responses request validator for the text/function subset and a
   fully buffered bounded Responses SSE parser.
2. Serve two deterministic loopback `/v1/responses` streams shaped from the
   official schema: one completed function call and one completed final text,
   each with distinct response IDs and complete usage.
3. Configure pinned OpenCode `1.18.29` with a typed local provider whose exact
   model override is `@ai-sdk/openai`; admit the configuration by readback.
4. Run one source-read tool loop, bind both provider requests and responses to
   the provider journal, and match the OpenCode transcript's tool call to the
   durable context-broker request and receipt.
5. Include adversarial fixture variants for model/ID substitution, broken event
   order, mismatched deltas and snapshots, incomplete/error terminals, missing
   usage, token overflow, duplicate terminal events and unrecognized trailing
   data.

That proves EngOrch's generic Responses transport against the pinned OpenCode
adapter. A later explicitly authorized opt-in request to each exact external
route is still required to establish credential eligibility, current endpoint
behavior, header acceptance and actual Muse response shape.

[zen]: https://opencode.ai/docs/zen/
[go]: https://opencode.ai/docs/go/
[zen-models]: https://opencode.ai/zen/v1/models
[go-models]: https://opencode.ai/zen/go/v1/models
[catalog-loader]: https://github.com/anomalyco/opencode/blob/16747470f976aca3d362ad730bcd3fe82ecc2c9a/packages/core/src/models-dev.ts#L160-L176
[provider-conversion]: https://github.com/anomalyco/opencode/blob/16747470f976aca3d362ad730bcd3fe82ecc2c9a/packages/opencode/src/provider/provider.ts#L1261-L1305
[zen-handler]: https://github.com/anomalyco/opencode/blob/16747470f976aca3d362ad730bcd3fe82ecc2c9a/packages/console/app/src/routes/zen/v1/responses.ts#L5-L13
[go-handler]: https://github.com/anomalyco/opencode/blob/16747470f976aca3d362ad730bcd3fe82ecc2c9a/packages/console/app/src/routes/zen/go/v1/responses.ts#L5-L13
[auth-source]: https://github.com/anomalyco/opencode/blob/16747470f976aca3d362ad730bcd3fe82ecc2c9a/packages/llm/src/route/auth.ts#L40-L48
[gateway-handler]: https://github.com/anomalyco/opencode/blob/16747470f976aca3d362ad730bcd3fe82ecc2c9a/packages/console/app/src/routes/zen/util/handler.ts#L124-L128
[responses-source]: https://github.com/anomalyco/opencode/blob/16747470f976aca3d362ad730bcd3fe82ecc2c9a/packages/llm/src/protocols/openai-responses.ts
[responses-reference]: https://platform.openai.com/docs/api-reference/responses-streaming/response/completed
[native-fixture]: https://github.com/anomalyco/opencode/blob/16747470f976aca3d362ad730bcd3fe82ecc2c9a/packages/opencode/test/fixtures/recordings/session/native-zen-tool-loop.json
[ai-sdk-map]: https://unpkg.com/@ai-sdk/openai@3.0.84/dist/index.mjs.map
