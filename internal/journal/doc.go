// Package journal owns durable canonical event chains in SQLite and retained
// legacy JSONL. It verifies integrity, not workflow authority. Controllers must
// validate event semantics inside Append's validator. See
// docs/specifications/run-journal.md.
package journal
