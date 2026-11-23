# candidatecopy: candidate-bound observation/copy helper

Standalone Go helper (package main) for Fabric v1 evaluation acceptance.

The separate v1.0.25 Sol High cohort used this candidate-bound path for seven
accepted tasks. A blocked or unreviewed candidate remains ineligible; the
godotenv failure was not copied or counted as accepted. See the
[current evaluation status](../README.md) and
[capability guide](../../../docs/guides/features.md).

- Inputs: `--snapshot <bounded candidate-copy projection JSON> --expected-candidate <64hex reviewed digest> --destination <fresh absolute external path>`.
- Decodes the runner's strict six-field projection (`run_id`, `state`, `workspace`, `candidate`, `verification`, and explicit `review`, including `null`) through `canonical.Decode`, then maps those values into the existing control snapshot validation path. It requires `Candidate.ID() == expected == verification.plan.candidate_id`, READY state, non-pending full-PASS observations, exact workspace binding, and review approval when present. The runner retains the complete inspect snapshot separately for evidence and gates.
- Holds `worktree.AcquireRead(snapshot.Workspace.Request)` across `Capture`, copy, and final fingerprint; requires actual `Candidate == snapshot.Candidate` before and after; never substitutes another workspace or run.
- Copies precisely the captured `FileStates` (tracked/untracked/ignored/deletions as fresh destination; `.git` already excluded by `Capture`) via `os.Root` + `safepath.CopyRegular` (64 MiB/file, 512 MiB total, 4096 files); preserves executable mode; rejects symlinks via `Capture`/`safepath` (no invented success).
- Destination must be new and external (both nesting directions rejected); parent symlinks rejected via `safepath.Directory`; never recursively deletes; failed copies retained as BLOCKED.
- Verifies destination `FilesID` equals source `FilesHash` with per-file hash+mode comparison; final source still expected under the read lease.
- Emits JSON `candidate_id/files_hash/file_count/destination/workspace/worktree_id`.

Build: `go build -o <exe> ./evals/v1/candidatecopy` (module `github.com/iancuuandrei/fabric`; no product files changed).
Test: `go test ./evals/v1/candidatecopy` (real worktree fixtures, no providers).
