# Repository identity v1

Scope: immutable run inputs. Repository identity records an operator-supplied
logical name, absolute checkout path, absolute Git common directory, object
format, exact commit and tree. Git observations MUST use direct argv with a
timeout and output bounds. A symbolic branch is never commit authority.

The name MUST be nonempty. Object IDs MUST match the declared SHA-1 or SHA-256
format. Paths MUST be absolute. Identity is a domain-separated SHA-256 digest
of the canonical record. Moving a checkout therefore changes its run identity.
An unborn repository or missing Git returns an error, never a fabricated commit.

Configuration and the objective are separate immutable inputs bound by the run
record. Repository content cannot authorize an effect. Discovery is read-only;
it does not fetch, checkout or modify Git. Concurrent changes are possible:
later mutation admission MUST re-observe identity. This discovery record is not
a writer lease or an OS security guarantee.

Canonical JSON v1 admits objects with unique ASCII member names, arrays, valid
Unicode strings, booleans, null and safe integers (absolute value at most
9007199254740991). Objects sort keys lexically; arrays retain order. No whitespace,
HTML escaping, floating point or Unicode normalization is used. Strings use
JSON escapes for quote, backslash and controls; other Unicode is UTF-8. Invalid
UTF-8, duplicate keys and unknown typed fields MUST fail. Typed field names are
case-sensitive; all nonoptional fields MUST exist with their declared types.
Null is allowed only for explicitly nullable fields. Bytes are hashed as
`domain + LF + canonical JSON`; domain is a nonempty ASCII identifier.

Version changes require new identities. No cross-version reinterpretation.
Example canonical value: `{"commit":"abc","version":1}` (illustrative only;
`abc` is not an admissible Git object ID).
