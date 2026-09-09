# Semantic candidate identity

New runs may explicitly select `candidate_identity = "semantic-index-v2"`.
Historical runs retain the v1 raw-index contract and its original hash domain.
The selection is bound into configuration, workspace request and candidate version.

V2 candidate identity binds workspace/repository identity, exact HEAD, the semantic
index digest, and the complete regular-file manifest (paths, bytes and executable
modes, including untracked and ignored files). The index digest covers exact
NUL-delimited `git ls-files --stage -z` and `git ls-files -v -z` outputs. This binds
staged paths, blobs, modes and stages, plus assume-unchanged/skip-worktree flags.
Together with HEAD and the file manifest these identify staged and worktree changes
without relying on a potentially filtered text diff. Stat/cache metadata is excluded.

The raw index SHA256 remains separate diagnostic evidence. Index observation checks
raw hashes before and after semantic Git reads; a concurrent rewrite fails closed.
Git reads disable optional locks. Existing leased controller gates compare the full
semantic candidate and journal `candidate.index-observed` with old/new raw hashes:

- `BASELINE`: initial observation of an admitted candidate;
- `UNCHANGED`: same raw and semantic evidence (no redundant event required);
- `METADATA_ONLY_INDEX_CHANGE`: raw hash differs, full semantic candidate identical;
- `CANDIDATE_MUTATION`: observed semantic candidate differs; gate returns an error.

These events do not replace candidate, plan, verification or effect authority.
Observation failures also block. A mutation is a gate failure, not authorization to
restore files, rebind identity or retry; the operator must stop the qualification run.
Baseline evidence is captured after workspace admission and successful governed file
effects. Writer, verifier, reviewer, file-effect and commit execution gates retain
raw observations. The same verification plan can run a benign Git verifier and
continue only after an unchanged semantic observation. D0g includes `git diff --check`
as a journaled verification command rather than an unrecorded operator action.

File/HEAD/staging mutations remain disallowed even within writable paths unless
admitted through the existing governed effect protocol. This option neither changes
model routing and proposal transport nor weakens repository/process isolation.
