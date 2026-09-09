# Context accounting

`harness runtime-usage JOURNAL` reports deterministic accounting for one validated
runtime journal. Relative paths resolve against the selected root. The command
returns no prompt, result body, file content or authentication data.

The report binds invocation/profile and journal head. It counts UTF-8 input/output
bytes, per-tool request arguments and response content bytes, requests, responses,
failures and pending calls. Tool content includes the actual serialized content
wrapper and any base64 representation recorded for the model. Failed responses
are counted because they also consume context. Stable tool-name ordering makes
repeated observations of the same journal reproducible.

Provider token/cost fields are preserved from an admitted completed result. Missing
values remain null. Byte totals are not tokenizer estimates and exclude hidden
provider prompts, automatic context, transport envelopes and other overhead.
No cost or efficiency claim follows merely from a byte total. Pending runtime
state can be reported, but is not labeled completed.

Executed on the retained authenticated modified-candidate writer fixture:
1415 input bytes, 238 output bytes and 587 tool-content bytes; candidate_list
provided 226 bytes and candidate_read 361 bytes, each with one successful request
and response. No calls remained pending. Provider input/output tokens and cost
were unavailable. These are observed fixture quantities, not a comparative
context-economics benchmark.

Regression coverage includes successful, failed and pending requests, matching
content byte totals, stable ordering and unknown usage. Broader workload comparisons,
latency/quality tradeoffs and token-level provider accounting remain unqualified.

`harness usage RUN` enumerates every Codex host intent in controller journal order, including earlier writer/reviewer attempts. Each available runtime report must match its invocation/profile; a controller runtime receipt additionally requires the identical completed journal head. Missing admitted journals or substituted heads fail the report. Missing journals for unadmitted host attempts are explicit entries with null usage. The scope is journaled_codex_hosts; fixture planning is not misrepresented as provider usage, and no aggregate unknown-token total is invented.

Executed against the retained authenticated repair/review run: both writer and reviewer journal heads matched controller receipts. Writer tool content was 226 bytes; reviewer tool content was 1163 bytes across candidate/base reads. Token/cost counts were unavailable. The exercise exposed an incompatible change to writer instruction text; restoring the earlier wording retained the new optional review field while making this prior journal replayable again. This one regression repair does not establish a general prompt-version migration policy.
