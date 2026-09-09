# Optional engineering procedures

The seven directories under `procedures/` are portable Agent Skills. Each contains
an independent `SKILL.md`; copy only the directories you want into your agent's
documented skill location, or read a procedure directly. No global installation,
controller router, supervisor, pstack or multi-agent setup is required. Installing
a procedure does not configure a runtime or authorize any effect.

| Procedure | Use it for | Output |
| --- | --- | --- |
| [how](../../procedures/how/SKILL.md) | Current implementation mechanics | Evidence-linked execution trace |
| [why](../../procedures/why/SKILL.md) | Decision rationale | Recorded reasons, alternatives and explicit hypotheses |
| [blast-radius](../../procedures/blast-radius/SKILL.md) | Proposed change impact | Consumers, breakage mechanisms and uncovered scope |
| [tdd](../../procedures/tdd/SKILL.md) | Behavioral implementation or regression repair | Observed failure, change and executed verification |
| [interrogate](../../procedures/interrogate/SKILL.md) | Consequential requirement ambiguity | Decisions and actionable acceptance criteria |
| [architect](../../procedures/architect/SKILL.md) | Architecture choices | Boundaries, contracts, failure cases and tradeoffs |
| [verification-design](../../procedures/verification-design/SKILL.md) | Evidence and qualification design | Claim-to-evidence matrix with independent oracles |

These methods can be combined when useful, but none invokes another by default.
The controller's authorization and evidence rules remain the execution authority.
In particular, a procedure's prose cannot convert UNKNOWN to success, approve a
plan, bypass a writer lease, or make an inferred dependency complete.

The packaging follows the minimal name/description frontmatter in the
[Agent Skills specification](https://agentskills.io/specification), consulted
2026-09-07. Provider-specific discovery and invocation behavior must be checked in
the chosen host. No cross-provider execution qualification is claimed merely from
valid frontmatter. See [procedure evaluation](../evaluation/procedures.md).
