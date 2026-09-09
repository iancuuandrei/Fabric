# Apply exact file changes

First approve a plan and create its workspace using the
[local planning guide](../getting-started/local-plan.md). The original checkout
remains separate from all writer changes.

Create a changes JSON file outside the writer workspace. This example adds a file
containing `hello` followed by a newline:

```json
[
  {
    "path": "added.txt",
    "before_hash": null,
    "content_base64": "aGVsbG8K",
    "executable": false
  }
]
```

For replacement or deletion, supply the current file's SHA-256 as `before_hash`.
Null `content_base64` deletes a file; an empty string creates an empty file.
The adapter supports binary bytes. Protected agent/control paths are rejected.

```sh
harness --root PATH prepare-files RUN changes.json > preview.json
```

Review the preview's ordered changes, complete before manifest, predicted after
candidate and `intent_id`. Preparing does not write to the workspace. Apply only
the reviewed identity with an explicit operator label:

```sh
harness --root PATH apply-files RUN preview.json INTENT_ID ACTOR
```

The result records CONFIRMED, NOT_APPLIED or UNKNOWN based on the whole observed
candidate. An execution error may follow partial writes; inspect the journal even
when the command exits unsuccessfully. An unchanged source checkout is never
substituted for evidence about the writer workspace.

```sh
harness --root PATH reconcile RUN
```

Reconciliation observes without retrying. It confirms complete after-state,
recognizes unchanged before-state, and leaves partial/unexpected state UNKNOWN.
No later proposal can bypass UNKNOWN. There is no automatic rollback or partial
retry. After NOT_APPLIED, prepare a fresh proposal and approve its new intent ID.
Multi-file application is not a filesystem transaction or an OS sandbox.
