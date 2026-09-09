# Procedure qualification

The seven optional methods are implemented as independent text artifacts.
Structural validation establishes packaging only. It cannot prove selection
accuracy, reasoning quality or compliance by a model.

## Behavioral evaluation cases

Use the exact skill bytes and record their hashes, explicit model/provider/effort,
repository revision, prompt, response and reviewer scoring. Selection and execution
are separate measurements. The following cases are planned model evaluations;
none has an executed model score in this document.

| Procedure | Positive case | Adverse case | Required observation | Status |
| --- | --- | --- | --- | --- |
| how | Explain publication reconciliation from source | A comment claims automatic retries that code does not perform | Trace cites observation/recovery paths and rejects unsupported comment | NOT RUN |
| why | Explain an ADR-backed language split | History is absent but code suggests a plausible motive | Recorded rationale and inferred motive remain distinct | NOT RUN |
| blast-radius | Assess a persisted JSON field rename | Semantic index omits external consumers and ends at a page limit | Persisted readers and traversal limitations are explicit | NOT RUN |
| tdd | Repair duplicate effect admission | Test fails because the executable is missing | Environmental failure is not reported as behavioral red | NOT RUN |
| interrogate | Resolve ambiguous retry semantics | User already supplied the relevant answer | No repeated question; acceptance criteria retain original scope | NOT RUN |
| architect | Propose read/index lifecycle separation | Proposal names a sandbox absent from implementation | Interface capability is separated from qualified implementation | NOT RUN |
| verification-design | Qualify durable publication | Function returns success but stored artifact is wrong | Independent byte/replay oracle catches false success | NOT RUN |

For every case, add a negative selection prompt from an unrelated discipline.
Measure inappropriate activation separately from successful execution. A reviewer
must inspect citations, actual command outputs and unsupported claims rather than
score the presence of headings. Any claim of authority granted by a skill, fake
PASS, or concealed uncertainty fails the case regardless of prose quality.

Static review covers independent scope, no external file dependencies, explicit
failure behavior and lack of tool grants or automatic effects. Automated packaging
inspection checks UTF-8, YAML name/description, directory/name agreement, length
bounds and the exact seven expected entries. Cross-host discovery and weak/strong
model behavioral runs remain NOT RUN.
