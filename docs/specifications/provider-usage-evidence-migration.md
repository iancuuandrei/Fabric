# Provider usage evidence migration

Status: implementation design, not an implemented receipt capability.

The adapter conformance requirement calls for normalized usage and cost together
with bounded provider usage evidence. Current CallReceipt v1 records normalized
tokens only. Adding an unvalidated raw blob would not satisfy that requirement.

## Required integrated change

Introduce a versioned receipt representation which retains an ordered collection
of usage fragments. Each fragment binds a finite protocol phase, exact byte
length and SHA-256, and stores only the native usage object. Store exact bytes
as a byte slice, not json.RawMessage: canonical journal serialization must not
rewrite whitespace in the evidence being hashed. Do not retain the enclosing
response, provider messages, reasoning, tool arguments or credentials here.

Validate duplicate keys, native usage shape, fragment count and total bytes.
The total bound must leave room below canonical.MaxBytes (1 MiB), including
base64 expansion and the rest of the receipt. Replay must rederive normalized
usage from the ordered fragments and compare every category, preserving unknown
versus reported zero. Hash verification alone is insufficient.

Chat and Responses have one terminal usage object. Anthropic streaming combines
message-start and message-delta usage; preserve order and apply the existing
native merge rules instead of summing cumulative output counters. Anthropic JSON
has one object. Decoder extraction and replay derivation must share the finite
native accounting rules.

Preserve legacy receipt bytes and v1 replay. New evidence-bearing receipts need
explicit version validation; do not silently allow v1 to carry unvalidated new
fields. Gateway replay must reject missing, changed or inconsistent evidence in
the new version. Clone evidence slices and their byte buffers independently.

Propagate evidence through ProviderResponseMetadata, providertransport.Execute,
CallReceipt, gateway replay and direct runtime Result. Recovery must return the
same evidence without another provider request. A nested receipt format change
must be checked against runtime result validation and full receipt equality.

## Cost boundary

Configured PricingPolicy is a reservation estimate, not observed expenditure.
ResponsesTrailingCostPing currently has no bound currency or scale and explicitly
is not billing evidence. Neither may populate observed cost implicitly.

Any observed monetary value must bind currency, integer unit scale, exact source
and a capability declaring that source. Decimal conversion must avoid floating
point, overflow and silent rounding. Missing cost remains unknown; subscription
usage must not invent API spend. Cost propagation must reach access accounting
and controller receipts, not stop at a decoder field.

## Qualification required before completion

- Positive and negative native evidence cases for all three protocols and both
  framings, including Anthropic cumulative/revised usage.
- Exact-byte preservation through canonical journal serialization and recovery;
  cloned evidence must not alias a prior snapshot.
- Rehashed but inconsistent evidence, duplicate keys, missing fragments, changed
  phase/order and oversized evidence must reject.
- Golden legacy receipt replay and new-version strict validation.
- Actual TLS completion returns only after durable receipt creation; offline
  recovery retains evidence and does not resend.
- Cost source/currency/scale, overflow, unavailable-versus-zero and subscription
  accounting cases when cost support is introduced.

This document is a migration contract. It does not close the current conformance
FAIL for raw usage and cost.
