---
name: anchored-editing
description: Produce exact, deterministic, conflict-free file edits using unique lexical anchors and verified preimages without truncation or unintended rewrites. Use whenever generating writer or fixer file proposals.
metadata:
  fabric.roles: "writer,fixer"
---

# Anchored Editing Discipline (Donor: Aider + SWE-agent ACI)

File-edit accuracy depends on exact lexical anchoring and zero-laziness diffs.

## 1. Read Exact Current Bytes First
- Never construct a search anchor from memory or an earlier candidate revision.
- Verify the exact file path, indentation (tabs vs. spaces), line endings, and current preimage hash before emitting edits.

## 2. Poka-Yoke Search-Replace Anchors
- Include enough unchanged leading/trailing lines so the `before` anchor matches **exactly once** in the target file.
- If a snippet appears multiple times (e.g., repeated `if err != nil` blocks), expand the anchor to include the enclosing function signature or unique surrounding statements.
- Keep each edit hunk minimal and focused; emit multiple small hunks rather than rewriting an entire large file.

## 3. Anti-Laziness & Atomic Proposals
- **Never elide code** with placeholders like `// ... existing code ...` or `# unchanged`.
- Use full-file creation only for genuinely new files; use anchored edits for existing files.
- Ensure every edit in a multi-file proposal is mutually consistent (imports, call sites, type signatures, and tests updated together in one atomic proposal).
- Reject no-op edits that leave file bytes unchanged.
