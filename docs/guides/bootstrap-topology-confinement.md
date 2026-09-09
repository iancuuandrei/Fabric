# Sol planner and Luna reviewer confinement

The bounded bootstrap topology uses independently qualified Codex profiles: GPT-5.6 Sol Medium planner and GPT-5.6 Luna Medium reviewer. Muse writer/fixer access is unchanged.

The existing Codex configuration path and SHA256 fields pin one finite topology manifest (`codex-registry-confinement-topology-r19`). It names exactly the planner and reviewer profiles, their dynamic-tool hashes, and separate relative leaf-manifest paths and SHA256 values. Each leaf is a complete ten-case registry qualification for its own model. Sol evidence is not used as Luna evidence.

Admission verifies the selected exact profile and dynamic tools, leaf bytes and raw proof, installed Codex identity, launch controls, model catalog record, and expiry. The stock runtime remains unchanged. A topology cannot reference another topology. Receipts remain bound to the controller-pinned top-level SHA; catalog and runtime constraints come from the selected validated leaf. Effective validity cannot extend beyond either the topology or leaf expiry.

This is a bounded bootstrap proof-selection layer, not a new model routing gateway. The prior single-model R17 manifest remains accepted for its original qualified profiles. Hooks remain diagnostic only; registry rejection and foreign-child detection provide the existing confinement behavior.

Offline qualification uses local canned provider responses. It proves the installed binary's capability behavior under the pinned configuration; it does not prove live Luna inference or usage. The real reviewer invocation must independently settle exact model identity, usage and review outcome before candidate commit.
