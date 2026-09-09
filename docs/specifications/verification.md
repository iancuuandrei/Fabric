# Verification v1

Scope: deterministic execution of all required checks against an exact candidate.
Each invocation binds the configured name/argv/timeout, absolute workspace,
candidate identity, explicit environment and its policy hash, and resolved root
executable path/hash where available. Commands use direct argv; no shell wrapping
or interpolation is implicit. Missing executables produce NOT_RUN, never PASS.

The environment policy is a finite versioned allowlist of platform/toolchain/cache
variables. Values are captured into the invocation; undeclared secrets and Git
override variables are not inherited. This is environment selection, not network
or process isolation. Root binary hashing is not transitive toolchain attestation.

Before dispatch the controller MUST persist exact invocation intent. Each result
records start/end, started flag, nullable exit code, timeout/cancellation, output
hashes and bounded excerpts, errors, and candidate identity. Each output stream
retains an 8 KiB excerpt and hashes up to 4 MiB. Overflow cancels execution and
marks the digest as a captured prefix; it can never yield PASS. Executables larger
than 256 MiB are unsupported. Missing dependencies inside a launched check yield
its actual failure, not an inferred successful skip.

The controller MUST bracket checks with candidate observations and preserve every
configured required check. Candidate drift blocks readiness. All required checks
must PASS on the same candidate before READY. Failure enters REPAIRING; unavailable
or unexecuted checks remain visible as NOT_RUN/BLOCKED. A missing result after a
started intent is uncertain and MUST NOT silently rerun a potentially effectful
check. Explicit observation/reconciliation is required.

Timeout/cancellation terminates the root process and attempts descendant cleanup.
Unix uses a process group; Windows uses bounded taskkill tree termination with
root-kill fallback. This is best-effort cleanup, not a security sandbox or proof
that arbitrary detached grandchildren cannot survive. Each result reports the
termination scope. A trusted operator must configure checks; an arbitrary check
can access its host user's files and network. WorkerEnvironment is a future
qualified boundary, not a capability implied by worktree separation.

`harness verify RUN` freezes the complete configured check sequence, executable
identities and selected environments in `verification.planned`. Each ordered
`verification.started` event precedes process launch; `verification.observed`
records the result and a fresh whole-worktree observation. The writer lease spans
the entire sequence. Replay rejects omitted/substituted checks and duplicate
launches. An unchanged candidate with all PASS observations enters READY; a
failed or unavailable check enters REPAIRING after the sequence. Source drift
stops dispatch immediately and enters REPAIRING without admitting changed files.

After a crash, `verify RUN` continues the same frozen plan only when no launch is
pending. It retains completed observations and starts at the first unstarted
check. An uncertain launch remains blocked regardless of elapsed time.

`close-verification RUN PLAN_ID ACTOR EVIDENCE workloads-stopped` records a human
attestation that the interrupted workload and descendants have stopped. The
controller acquires the writer lease and verifies unchanged source. It does not
infer process termination, steal stale leases or manufacture a result. The
original pending flag and missing observation remain visible with the closure;
the run enters REPAIRING. A subsequent explicit `verify` creates a fresh full
attempt. This operation trusts the named operator's evidence; it is not automatic
process reconciliation. A source mismatch requires separate repair and cannot be
admitted by closure. The current plan ID is exposed in inspection output.
Output qualification is local process evidence; it does not qualify a real model
provider, Rust RI, hosted CI, or production isolation.
