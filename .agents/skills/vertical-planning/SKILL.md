---
name: vertical-planning
description: Decompose engineering objectives into small, verifiable vertical tracer-bullet slices with explicit separation between dependency, coupling, and write ownership. Use when planning multi-file or multi-task work.
metadata:
  fabric.roles: "planner"
---

# Vertical Planning & Safe Decomposition (Donor: Superpowers + CoCoder + Pact + pi-subagent-tasks)

Good decomposition makes implementation and verification trivial; bad decomposition causes merge conflicts, broken intermediates, and wasted tokens.

## 1. Prefer Vertical Slices Over Horizontal Layers
- Break work into thin, end-to-end verifiable slices (e.g., one API endpoint + handler + test) rather than broad horizontal layers (e.g., "all models", then "all controllers").
- Keep the task graph minimal: do not split a cohesive 2-file change into multiple writers just to look parallel.

## 2. Separate Dependency, Coupling, and Ownership
Treat these three concepts strictly separately:
- **Dependency (Readiness):** Task B requires Task A's types, schema, or API to exist first → declare an explicit edge `A -> B`.
- **Coupling (Interference Risk):** Two tasks touch different files but share a hub interface, config schema, code generator, or shared test fixture → **serialize them** (`hub first -> independent leaves concurrently`).
- **Write Ownership (Authority):** Every concurrent task must own a **strictly disjoint** set of write paths. Never assign the same file or generator family to two parallel writers.

## 3. Right-Size Each Task Brief
Every task in the plan must specify:
1. Exact objective and observable acceptance criterion;
2. Exact owned write paths (minimal set);
3. Read-only reference paths / invariants that must not break;
4. Explicit dependencies on upstream tasks.
