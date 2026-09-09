# Codex host admission v1

Scope: local process/configuration preparation. `internal/codexhost.Prepare`
requires an existing empty control directory and an absolute executable. It
creates a separate home and startup workspace, writes a fixed configuration, and
binds configuration and executable SHA-256 into a launch identity. Existing
contents and subsequent drift are rejected. Executables are bounded to 512 MiB;
the inspected desktop binary is 295,408,944 bytes.

The child inherits a finite list of platform/path variables and its own CODEX_HOME.
API keys, Git overrides and arbitrary environment variables are not inherited.
No user authentication/configuration files are copied by preparation. This is environment
selection, not proof about operating-system credential services.

An explicit `LoginChatGPT` call can read an existing absolute authentication file
and supply only its access token and account identity over the external-token
protocol. It verifies a bounded regular file, rejects malformed or changing input,
and does not transmit refresh/ID tokens or rewrite the source. Credentials do not
enter runtime journals or launch identities. Automatic token refresh is not
implemented; a refresh request is refused by the transport. This helper requires
the caller to own the user's authentication lifecycle.

Start repeats restrictions as CLI overrides, initializes the owned stdio server,
checks its returned home, and reads effective configuration and feature state.
It requires approval policy `never`, sandbox mode `read-only`, disabled web search,
26 disabled feature controls, enabled `skip_host_skill_discovery`, and an empty
MCP inventory. Unexpected startup requests/notifications are rejected; the known
remote-control notification is accepted only when disabled and without a remote
environment identity. No model turn occurs during admission.

## Native execution environments

The installed experimental ThreadStartParams schema states that an empty
`environments` array disables environment access, while omission selects a
default. The runtime explicitly sends empty arrays on thread and turn creation,
empty selected capability roots and disallowed provider model fallback. The host
negotiates experimental protocol support. This version-specific policy must be
requalified when the executable changes.

The `unified_exec` selector cannot be disabled by ordinary configuration in this
build and is not used as an admission control. `shell_tool` must be observed false
and native environments are explicitly absent. Source inspection supports that
shell registration requires ShellTool and environment access, while apply-patch
registration independently requires environment access. This is source/protocol
evidence, not a completed adversarial model-tool experiment.

Close releases pipes and reaps the root process; it does not certify descendant
confinement. Controller planning and the two scoped dynamic source tools have
executed fixture evidence. Thread-specific inventory and adversarial native-tool
qualification remain necessary for the complete runtime boundary.
