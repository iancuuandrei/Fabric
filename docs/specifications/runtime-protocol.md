# Runtime capability contract v1

Scope: finite agent invocation, not a general provider SDK. A runtime exposes
capabilities, Execute, Resume, Cancel, Close and Usage. Unsupported operations
return explicit errors. Requested profile records runtime, provider, model,
effort and role. Roles are planner, explorer, writer and reviewer.

Each invocation binds input text and its canonical digest plus the exact profile.
Results MUST retain invocation identity, requested profile, nullable observed
model, provider and effort, output and usage. Any observed routing mismatch MUST
fail admission. Missing
observed identity is UNKNOWN, never silently filled with requested identity.
Capabilities state whether observation is required; the fake runtime requires it.
Unknown token counts and cost are null rather than zero. Cost uses an explicit
currency and integer minor-unit scale when introduced, never floating point.

Input/output text is bounded to 256 KiB each. Profiles reject empty identifiers
and unknown roles. Cancellation is an explicit error; a runtime must honor an
already-cancelled context before work. Capabilities describe supported behavior,
not certification of a sandbox. The initial fake runtime has no process,
filesystem, network or external-effect authority and returns a deterministic
planning response. It does not simulate production isolation. Its Cancel method
has no asynchronous target, so the cancellation capability is false even though
Execute honors a cancelled context.

Runtime and RI protocol versions are independent. Provider adapters must validate
their own wire protocol and translate into this contract without granting new
authority. v1 typed inputs reject unknown fields. No real provider is implied
by the existence of this interface.
