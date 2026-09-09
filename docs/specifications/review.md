# Optional reviewer gate

A configured reviewer changes successful verification's destination from READY
to REVIEWING. Without a reviewer, existing verification behavior is unchanged.
Review input binds the explicit reviewer profile, current candidate, approved
plan and complete verification-evidence identity. Every required check must have
a fresh observed PASS for that candidate before review preparation is admitted.

The verdict contains candidate_id, verification_plan_id, decision and findings.
Decision approve requires an empty findings array. Decision changes_requested
requires one or more concrete findings, each with a relative path (or empty for
a cross-cutting concern) and message. Findings are bounded to 64 entries of at
most 4096 message bytes. Unknown fields and inconsistent decisions fail decoding
or admission. Approval transitions to READY; requested changes return to REPAIRING.
Neither outcome grants authority to publish or changes verification evidence.

Current executed evidence uses explicit fake-reviewer results and real Git
verification processes: both transitions pass, substituted candidate/verification
IDs and contradictory approvals fail, and repetition outside REVIEWING fails.
Provider-backed verdict admission requires a private reviewer host observation and
an exact runtime journal/result receipt. `prepare-review RUN` prints the invocation;
`review RUN` executes or resumes the read-only adapter, checks candidate freshness
before and after the call and admits the verdict under a reacquired workspace lease.
Its host and thread are separate from the writer. Candidate tools expose the current
verified state; source tools are for base comparison. No review operation applies
file changes or publishes resources.

A confirmed RI publication is included in review input and invocation identity
with explicit base-commit scope. Dispatch revalidates the stored snapshot and
pinned executable, enables the RI tools and requires the runtime's exact binding
before admitting its result. Current candidate changes are not indexed by this
snapshot. Actual Rust publication/input-identity tests and authenticated
writer/reviewer RI tool use pass on the bounded SCIP fixture.

The input provides verification_plan_id explicitly, separate from implementation
plan_id. A first authenticated fixture returned the implementation plan ID in the
verification field and was rejected. The strict identity check was preserved; the
input/prompt were clarified before another qualification attempt. See current
executed outcomes in the [evidence status](../evaluation/status.md).

After changes_requested, writer input includes the review invocation, reviewed candidate, verification plan, decision and findings. Messages have bounded UTF-8-safe prefixes with an explicit shortened flag; a hash of the full review record binds omitted tails and all runtime metadata. The prompt treats findings as advisory evidence without effect authority and identifies them as potentially referring to an earlier candidate. The real-Git/fake-reviewer rejection test checks exact feedback in the next writer input and verifies that removing the review changes invocation identity. An authenticated fixture now covers negative review, feedback-bound writer repair, separately authorized application, re-verification and a second approving review. Its git-version verifier is intentionally insufficient to detect the content defect; the fixture independently checks the corrected exact bytes. This establishes the bounded feedback loop, not broad review quality.
