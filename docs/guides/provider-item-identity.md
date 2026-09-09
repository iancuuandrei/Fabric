# External provider item identity

EngOrch-owned identifiers and resource locators retain their strict syntax.
Provider-owned Responses item identifiers are opaque values in a distinct
`ProviderItemID` domain. They are not session, message, run, or filesystem
locators.

A provider item identifier is nonempty UTF-8 text of at most 256 bytes. It is preserved
exactly, including punctuation and Unicode; it is not trimmed, normalized,
URL-decoded, or sanitized. Control characters are rejected. Validation grants
no execution, retry, filesystem, or settlement authority.

The named type requires explicit conversion to a Go string. Such conversion
must not make the value a filesystem path or URL path segment. If an artifact
key is needed, derive a SHA-256 key from the original bytes and retain the raw
identifier separately. R12 introduces no artifact filename use.

The JSON representation remains a string, preserving historical receipt bytes
for previously accepted values. OpenCode session/message locators continue to
use the original stricter validation. This bounded correction covers Responses
item metadata in tool, text, reasoning and composite observations; it is not a
redesign of every provider identifier in the system.

The motivating M1b observation contained a colon in a native reasoning item ID.
M1b remains a frozen failed qualification; accepting its identifier in a later
parser does not retroactively accept or settle that invocation.
