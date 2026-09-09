# Optional exploration

Exploration answers a bounded reading question using the explicitly configured
explorer profile. Missing configuration never falls back to another role.
Preparation binds question, run, approved plan, objective, current candidate and
any confirmed base-commit RI snapshot. It does not dispatch a model.

The output contract contains `candidate_id`, a nonempty summary of at most 8192
UTF-8 bytes and at most 64 sorted unique relative paths. Paths are model claims,
not verified citations. Results are advisory synthesis and grant no permission
or verification status. A versioned v2 exploration policy admits 1 to 256
records per run and projects at most 64 into writer context. When the policy is
absent, both bounds remain 16 for replay and configuration compatibility.

`RecordExploration` holds the candidate lease, checks live freshness and appends
`explorer.recorded`. Replay reconstructs the expected invocation and validates
the exact requested and observed model, candidate and result shape. Duplicate
invocations, substituted questions and stale candidates fail. Admission leaves
the workflow state unchanged. Codex results require the matching observed host and runtime receipt, including
the result hash. Model-shaped output without that evidence is rejected.

Writer context retains the records in journal order, with invocation and full
evidence hashes, original candidate identity and `candidate_current`. Question
and summary excerpts each use a 384-byte UTF-8-safe prefix; path strings share a
1024-byte per-record budget. `shortened` exposes omissions, whose exact contents
remain bound by the full evidence hash and available in the run journal.
The writer projection retains the configured `max_context_records` most recent
records, in journal order (16 when the exploration policy is absent). If
the retained history exceeds that window, `omitted_records` gives the omitted
count and `omitted_evidence_hash` binds the ordered full evidence identities of
the omitted prefix. Changing omitted content or its order changes writer input.
Both fields are absent when nothing is omitted, preserving the legacy shape.
Historical records are not relabeled as current after an authorized file change.
The optional input field is absent when there are no records.

The writer retains direct candidate, source and RI access. Exploration cannot
hide files, gate source inspection, establish facts or authorize effects.

Real Git fixtures pass admission/replay, explicit routing, invalid output and
live-drift rejection, duplicate rejection, writer identity changes including an
omitted evidence tail, and historical marking after a governed file change.
`prepare-explorer RUN QUESTION` prints the invocation; `explore RUN QUESTION`
executes or resumes a private host. Candidate and optional RI bindings are
verified before dispatch and result admission. Runtime receipts bind exact
journal heads, and usage includes historical explorer hosts. Current execution
uses a shared candidate lease for explorer/reviewer work. Governed mutations
and writer/fixer execution retain an exclusive lease. The cross-process guard
permits simultaneous readers, excludes readers and writers from overlapping,
and preserves stale writer-token recovery requirements. Candidate identity and
freshness checks still apply before admission. Windows guard concurrency tests
are executed evidence; full parallel runtime throughput remains separately
qualified in the evaluation reports.

Authenticated CLI qualification passes for an explorer reading a modified
candidate followed by a governed source change, historical-context delivery and
writer re-reading the new bytes. Both roles' tool calls and runtime receipts are
checked. Two still-current-context attempts failed the independent-read assertion;
they remain evidence of model behavior limits, not successful qualification.
See [evaluation status](../evaluation/status.md) for the executed scope.
