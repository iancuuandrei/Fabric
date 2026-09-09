# Explicit planner assignment

`planner_contract = "plan-v1"` opts a new run into an explicit planner-only assignment. The planner receives a canonical envelope containing its role, the planning instruction and the exact original objective. Read-only source access is expected; editing and verification belong to later roles. The wrapper does not grant tools or effects.

The empty setting retains the historical invocation bytes and identity. Unknown values fail closed. The chosen setting is part of immutable run configuration, and dispatch, replay, access settlement, scheduled identity reconstruction and usage accounting use the same planner invocation constructor.

The writer receives the original objective plus the approved plan through its existing implementer contract. Planner-specific instructions are not inserted into writer/fixer prompts. This clarifies role responsibility without changing the task requirements or writable scope.

A completed model invocation is not automatically an acceptable plan. The operator's exact plan gate still rejects responses that request capabilities instead of proposing the bounded implementation. Bootstrap goal authority permits the operator to approve a conforming plan locally; it does not permit scope expansion.
