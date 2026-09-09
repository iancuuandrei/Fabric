# Native OpenCode text phase metadata

M2i completed eight validated provider requests, including a final stop response. Native tool-turn normalization then rejected two intermediate text parts whose OpenAI metadata contained `itemId` and `phase: commentary`. The final text part contained only `itemId`. The prior normalizer required exactly one metadata key and therefore rejected legitimate text phase metadata.

Text phase is finite descriptive provider metadata, not a tool capability, path, or effect authority. Preserve absent versus explicit phase evidence in the typed native observation. Accept only null, commentary, or final_answer where the protocol permits it; reject unknown values and fields. Do not extend acceptance to tool-call metadata or reasoning metadata. Preserve legacy identities when the new field is absent.

The raw provider response and native database remain private frozen M2i evidence. M2i is NO-GO; successful provider completion does not retroactively settle the blocked native invocation. A fresh qualification run is required after regression and independent review.
