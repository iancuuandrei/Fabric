package agentloop

import (
	"fmt"
	"strings"
)

// Capabilities encodes typed tool permissions enforced outside the LLM context
// window (CapScope pattern, arXiv:2609.08371) so untrusted repository files or
// prompt injections can never expand runtime authority.
type Capabilities struct {
	AllowRead    bool     `json:"allow_read"`
	AllowWrite   bool     `json:"allow_write"`
	AllowDelete  bool     `json:"allow_delete"`
	AllowCommand bool     `json:"allow_command"`
	WriteRoots   []string `json:"write_roots,omitempty"`
}

// DefaultCapabilities derives explicit runtime capabilities from worker flags.
func DefaultCapabilities(allowCommand bool, allowedWrite []string) Capabilities {
	roots := append([]string(nil), allowedWrite...)
	return Capabilities{
		AllowRead:    true,
		AllowWrite:   true,
		AllowDelete:  true,
		AllowCommand: allowCommand,
		WriteRoots:   roots,
	}
}

// ReadOnlyCapabilities constructs strict read-only inspection capabilities.
func ReadOnlyCapabilities() Capabilities {
	return Capabilities{
		AllowRead:    true,
		AllowWrite:   false,
		AllowDelete:  false,
		AllowCommand: false,
	}
}

func (c Capabilities) checkToolAllowed(name string) error {
	switch name {
	case "repo_map", "graph_impact", "read_file", "list_files", "search_text", "git_diff", "list_skills", "load_skill":
		if !c.AllowRead {
			return fmt.Errorf("capability denied: read tools disabled for %s", name)
		}
	case "edit_file", "write_file":
		if !c.AllowWrite {
			return fmt.Errorf("capability denied: write tools disabled for %s", name)
		}
	case "delete_file":
		if !c.AllowDelete || !c.AllowWrite {
			return fmt.Errorf("capability denied: delete_file disabled")
		}
	case "run_command":
		if !c.AllowCommand {
			return fmt.Errorf("capability denied: run_command is disabled")
		}
	}
	return nil
}

// fenceUntrustedRepoGuidance wraps repository-local documentation, skills, or
// lessons in explicit boundary tags so instructions cannot override Fabric's
// controller safety and verification policies (repo-poisoning defense).
func fenceUntrustedRepoGuidance(source, content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	safeSource := strings.NewReplacer(`"`, `'`, "\n", " ", "\r", "").Replace(strings.TrimSpace(source))
	safeBody := strings.ReplaceAll(trimmed, "</untrusted_repo_context>", "< /untrusted_repo_context >")
	return fmt.Sprintf("<untrusted_repo_context source=%q>\n%s\n</untrusted_repo_context>", safeSource, safeBody)
}
