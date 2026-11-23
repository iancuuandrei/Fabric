# Engineering skills

Fabric ships **32 built-in SWE engineering skills** embedded directly into the
compiled `fabric` binary (see [`builtin_skills.go`](../../builtin_skills.go)),
so every external repository gets the full catalog automatically with zero setup.
Repository-local `.agents/skills/<name>/SKILL.md` files always take precedence
when present; export defaults into any repo with:

```powershell
fabric skills list
fabric skills show modular-design-and-boundaries
fabric skills install --root /path/to/repo            # install all 32
fabric skills install code-hygiene api-and-interface-ergonomics
```

Fabric captures the committed `.agents/skills/` catalog for autonomous runs,
filters discovery by role and loads full bodies lazily on demand. See
[agent context resolution](agent-context.md) for scope, source identity, replay,
and bounds.

## Discovery, Context & Architecture

| Skill | Roles | Method |
| --- | --- | --- |
| [architecture](../../.agents/skills/architecture/SKILL.md) | planner, explorer, reviewer | Boundaries, interfaces and consequential tradeoffs |
| [blast-radius](../../.agents/skills/blast-radius/SKILL.md) | planner, explorer, fixer, reviewer | Consumers, regressions and uncovered scope |
| [brownfield-convention-matching](../../.agents/skills/brownfield-convention-matching/SKILL.md) | planner, explorer, writer, fixer | Peer discovery, shared helper reuse & brownfield idiom matching |
| [codebase-trace](../../.agents/skills/codebase-trace/SKILL.md) | explorer, writer, fixer | Evidence-linked implementation trace |
| [context-hygiene](../../.agents/skills/context-hygiene/SKILL.md) | planner, explorer, writer, fixer, reviewer | Observation-masking & anti-context-rot token discipline |
| [fault-localization](../../.agents/skills/fault-localization/SKILL.md) | planner, explorer, fixer | Hierarchical repo/file/span fault localization |
| [graph-navigation](../../.agents/skills/graph-navigation/SKILL.md) | planner, explorer, writer, fixer, reviewer | Graph-first discovery & `EXTRACTED`/`INFERRED` edge discipline |
| [modular-design-and-boundaries](../../.agents/skills/modular-design-and-boundaries/SKILL.md) | planner, writer, fixer, reviewer | Deep modules, acyclic inward dependencies & functional core |
| [working-context](../../.agents/skills/working-context/SKILL.md) | explorer | Bounded non-authoritative working notes between turns |

## Planning, Specs & Repo Integration

| Skill | Roles | Method |
| --- | --- | --- |
| [api-and-interface-ergonomics](../../.agents/skills/api-and-interface-ergonomics/SKILL.md) | planner, writer, fixer, reviewer | Narrow consumer interfaces, REST/CLI envelopes & provider adapters |
| [decision-rationale](../../.agents/skills/decision-rationale/SKILL.md) | planner, explorer, reviewer | Recorded reasons, alternatives and hypotheses |
| [repo-onboarding-and-integration](../../.agents/skills/repo-onboarding-and-integration/SKILL.md) | planner, explorer, writer | Ground-truth commands, concise `AGENTS.md`, zero-secret MCP/CI setup |
| [requirement-analysis](../../.agents/skills/requirement-analysis/SKILL.md) | planner, explorer | Consequential ambiguity and acceptance criteria |
| [spec-driven-dev](../../.agents/skills/spec-driven-dev/SKILL.md) | planner, writer | Compact pre-implementation contract & YAGNI gate |
| [vertical-planning](../../.agents/skills/vertical-planning/SKILL.md) | planner | Vertical tracer-bullet slices & disjoint write ownership |

## Implementation, Clean Code & Repair

| Skill | Roles | Method |
| --- | --- | --- |
| [anchored-editing](../../.agents/skills/anchored-editing/SKILL.md) | writer, fixer | Surgical unique-anchor edits & line-ending fidelity |
| [clean-code-and-control-flow](../../.agents/skills/clean-code-and-control-flow/SKILL.md) | writer, fixer, reviewer | Edge parsing ("parse don't validate"), early returns & stdlib-first |
| [error-handling-and-observability](../../.agents/skills/error-handling-and-observability/SKILL.md) | writer, fixer, reviewer | Wrapped errors, retry classification & privacy-safe telemetry |
| [repair](../../.agents/skills/repair/SKILL.md) | fixer | Bounded correction from failed-gate evidence |
| [structural-refactoring](../../.agents/skills/structural-refactoring/SKILL.md) | writer, fixer | Cross-file symbol enumeration & compiler-checked edits |
| [systematic-debugging](../../.agents/skills/systematic-debugging/SKILL.md) | explorer, fixer | Reproduction-first root-cause debugging |

## Code Hygiene, Contracts & Safety

| Skill | Roles | Method |
| --- | --- | --- |
| [ci-and-supply-chain-hygiene](../../.agents/skills/ci-and-supply-chain-hygiene/SKILL.md) | planner, writer, fixer, reviewer | SHA-pinned Actions, locked builds, safe exec & fail-closed auth |
| [code-hygiene](../../.agents/skills/code-hygiene/SKILL.md) | writer, fixer, reviewer | Zero bloat, low duplication, dead-code removal & vet/fmt gates |
| [concurrency-resource-safety](../../.agents/skills/concurrency-resource-safety/SKILL.md) | writer, fixer, reviewer | Context propagation, `defer cancel/Close`, bounded reads & `-race` |
| [performance-and-profiling](../../.agents/skills/performance-and-profiling/SKILL.md) | explorer, writer, fixer, reviewer | Measure-first benchmarks, allocation deltas & queue/I/O bounds |
| [safe-migrations-and-contracts](../../.agents/skills/safe-migrations-and-contracts/SKILL.md) | planner, writer, fixer, reviewer | Expand-contract DTO/OpenAPI/config evolution & replay safety |

## Testing, Verification & Audit

| Skill | Roles | Method |
| --- | --- | --- |
| [code-review](../../.agents/skills/code-review/SKILL.md) | reviewer | Precision-first diff review & filter-only reflection |
| [property-differential-testing](../../.agents/skills/property-differential-testing/SKILL.md) | writer, fixer, reviewer | Property, differential, metamorphic & mutation-resistant tests |
| [security-audit](../../.agents/skills/security-audit/SKILL.md) | reviewer, planner, explorer | Injection, path confinement, SSRF, secrets & concurrency audit |
| [tdd](../../.agents/skills/tdd/SKILL.md) | writer, fixer | Discriminating regression and correction workflow |
| [verification-before-completion](../../.agents/skills/verification-before-completion/SKILL.md) | writer, fixer, reviewer | Fresh post-edit check evidence, negative paths & honest verdicts |
| [verification-design](../../.agents/skills/verification-design/SKILL.md) | planner, reviewer | Claim-to-evidence matrix and independent oracles |

No skill grants permissions, credentials, tool capabilities or effect authority.
Use host-native discovery only according to that host's documented behavior.
