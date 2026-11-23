---
name: context-hygiene
description: Anti-context-rot and token-economy protocol grounded in NeurIPS Observation Masking, Context-Folding, and Compaction Cliff research. Use throughout long trajectories to avoid redundant reads and repeated tool loops.
metadata:
  fabric.roles: "planner,explorer,writer,fixer,reviewer"
---

# Context Hygiene & Token-Economy Protocol

Long agentic trajectories degrade when stale tool logs crowd out core invariants ("context rot" and the "compaction cliff"). Keep the working context sharp and surgical.

## 1. Signal-to-Noise Discipline
- Read exact line ranges (`start_line`, `end_line`) instead of repeatedly dumping full 1,000-line files.
- Never paste redundant file bodies or verbose logs into reasoning prose; cite exact `path:line` anchors.
- Keep responses and tool arguments concise and direct — zero conversational filler.

## 2. Loop & Stale-Hypothesis Prevention
- Track failed hypotheses explicitly: if an edit or command fails twice for the same root cause, STOP editing and re-read the exact failing bytes and test error before attempting a third patch.
- Never repeat an identical tool call whose inputs and underlying workspace state have not changed.
- When Fabric's observation masker replaces older verbose outputs, re-read only the single narrow span you actively need rather than reloading entire directories.
