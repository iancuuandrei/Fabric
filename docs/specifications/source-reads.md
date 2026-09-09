# Committed source reads v1

`repository.ReadSource` reads a regular Git blob from the run's recorded commit,
validates the corresponding tree, and returns a bounded byte interval. Working
tree edits are irrelevant. A committed symlink is rejected rather than followed.
Git replacement objects and lazy fetching are disabled for these observations.

Each response records repository identity, commit, exact relative path, blob ID,
offset, full blob size, base64 bytes, fragment SHA-256 and nullable next offset.
`content_utf8` contains exactly the fragment text when the entire fragment is
valid UTF-8; otherwise it is null. No replacement decoding occurs, including
when a page boundary splits a multibyte character. Base64 remains authoritative.
Reads accept 1–32,768 bytes per call; files above 64 MiB are explicitly unsupported.
The reader drains the blob with bounded retained memory and a ten-second process
timeout. This bounds memory, not total I/O across repeated paginated requests.

`repository.ListSource` streams the same immutable tree and returns 1–128 leaves
in global bytewise path order, with an exclusive `after` cursor and nullable
continuation. It retains only the first `limit+1` eligible leaves rather than the
whole tree. Symlinks, submodules and unknown kinds remain explicitly identified
in the listing. No paths are ranked by relevance. More than 200,000 leaves,
paths over 4,096 bytes, non-UTF-8 paths or incomplete output fail the entire call.
Each page scans the tree again under a ten-second timeout; this bounds retained
memory rather than aggregate pagination I/O. Tests cover Git traversal order
differing from global path order, dirty checkout deletion, split stream records,
unsupported kinds and resource bounds.

This is source-access infrastructure, not semantic RI or relevance filtering.
The planner exposes it through `source_list` and `source_read`. Tests prove binary
round trips across pages, immutable reads despite dirty checkout contents, and
rejection of missing/traversing paths or offsets beyond the blob.

The broker records source identity before thread creation and tool requests before
reading. It records exact responses before sending them. Requests must match the
recorded thread, active turn and one of these two tool names, with no namespace.
An early request before the turn-start response binds a provisional tool turn ID
which the later dispatch receipt must match. Duplicate call IDs, foreign scopes
and unresolved requests stop execution. Invalid source arguments return a
recorded failed response so the model can make a new corrected read request.
Bounds are 16 KiB per incoming request, 512 responses and 8 MiB of total response
content per invocation, in addition to source, transport and journal bounds.

PASS: an authenticated CLI fixture on 2026-09-07 used both source tools and returned
an unpredictable committed value despite a dirty checkout decoy. The runtime
journal contains successful tool responses; completed resume was unchanged.
This proves the tested source path, not adversarial native-tool isolation or
implementation/repair/review automation.
