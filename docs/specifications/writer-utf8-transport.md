# Writer UTF-8 transport

Set `writer_contract = "utf8-v2"` explicitly for a new run. Writer and fixer return
`candidate_id` and 1–64 sorted, unique `changes`, each containing `path`,
`before_hash`, `content_utf8`, and `executable`. `content_utf8` is a JSON string
containing source text; null means deletion, while an empty string means an empty
file. No Base64 generation or execution tool is required from the model.

The controller validates canonical JSON (including malformed Unicode and duplicate
keys), decodes the text, and Base64-encodes its exact UTF-8 bytes for the existing
file-effect contract. It retains the original model response in the runtime and
writer receipts. Journal replay repeats the same conversion and compares the
prepared effect, preserving candidate identity and approval authority.

This does not remove file, JSON, stream, or provider limits. The existing 350000
character internal Base64 bound per file and aggregate effect limits still apply.
`unlimited_tokens` controls token ceilings only. Historical `nonempty-v1` and
legacy runs retain their original contracts; there is no automatic fallback to
Base64 or repair of malformed output. Binary model-authored changes are outside
this text-only contract.
