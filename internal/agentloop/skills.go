package agentloop

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	fabricassets "github.com/iancuuandrei/fabric"
)

const (
	maxGuidanceBytes = 16 << 10
	maxSkillBodyByte = 24 << 10
)

// SkillMetadata describes one lazily loadable SKILL.md discovered in
// .agents/skills/, an explicit skill directory, or Fabric's embedded catalog.
type SkillMetadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source,omitempty"`
	Path        string `json:"-"`
	embedPath   string
}

// DiscoverSkills lists sorted repository skills overlaid on top of Fabric's
// built-in embedded skill catalog.
func DiscoverSkills(repoRoot string, extraDirs ...string) []SkillMetadata {
	return discoverSkills(defaultSkillSearchDirs(repoRoot, repoRoot, extraDirs)...)
}

// LoadSkillBody reads the full SKILL.md markdown body for name from disk or
// from Fabric's embedded catalog.
func LoadSkillBody(skills []SkillMetadata, name string) (string, bool) {
	return loadSkillBody(skills, name)
}

// InstallBuiltinSkills writes selected embedded SKILL.md files (or all of them
// when names is empty) into repoRoot/.agents/skills/<name>/SKILL.md.
func InstallBuiltinSkills(repoRoot string, names []string, overwrite bool) (installed []string, skipped []string, err error) {
	if strings.TrimSpace(repoRoot) == "" {
		return nil, nil, fmt.Errorf("repoRoot is required")
	}
	builtins := discoverSkills()
	byName := make(map[string]SkillMetadata, len(builtins))
	for _, sk := range builtins {
		byName[strings.ToLower(sk.Name)] = sk
	}

	var targets []SkillMetadata
	if len(names) == 0 {
		targets = builtins
	} else {
		seen := map[string]bool{}
		for _, rawName := range names {
			clean := strings.ToLower(strings.TrimSpace(rawName))
			if clean == "" || strings.ContainsAny(clean, `/\`) || clean == "." || clean == ".." {
				return nil, nil, fmt.Errorf("invalid skill name %q", rawName)
			}
			sk, ok := byName[clean]
			if !ok {
				return nil, nil, fmt.Errorf("unknown built-in skill %q (run `fabric skills list` to see available skills)", rawName)
			}
			if !seen[sk.Name] {
				seen[sk.Name] = true
				targets = append(targets, sk)
			}
		}
	}

	for _, sk := range targets {
		if sk.Name == "" || strings.ContainsAny(sk.Name, `/\`) || sk.Name == "." || sk.Name == ".." {
			return nil, nil, fmt.Errorf("invalid embedded skill name %q", sk.Name)
		}
		raw, rErr := fs.ReadFile(fabricassets.BuiltinSkillsFS, sk.embedPath)
		if rErr != nil {
			return nil, nil, fmt.Errorf("read embedded skill %s: %w", sk.Name, rErr)
		}
		dst := filepath.Join(repoRoot, ".agents", "skills", sk.Name, "SKILL.md")
		if _, statErr := os.Stat(dst); statErr == nil && !overwrite {
			skipped = append(skipped, sk.Name)
			continue
		}
		if mkErr := os.MkdirAll(filepath.Dir(dst), 0o755); mkErr != nil {
			return nil, nil, fmt.Errorf("mkdir skill %s: %w", sk.Name, mkErr)
		}
		if wErr := os.WriteFile(dst, raw, 0o644); wErr != nil {
			return nil, nil, fmt.Errorf("write skill %s: %w", sk.Name, wErr)
		}
		installed = append(installed, sk.Name)
	}
	return installed, skipped, nil
}

func discoverSkills(dirs ...string) []SkillMetadata {
	seen := map[string]bool{}
	var out []SkillMetadata
	for _, base := range dirs {
		base = strings.TrimSpace(base)
		if base == "" {
			continue
		}
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			skillPath := filepath.Join(base, ent.Name(), "SKILL.md")
			raw, err := os.ReadFile(skillPath)
			if err != nil || len(raw) == 0 || len(raw) > maxSkillBodyByte {
				continue
			}
			name, desc := parseSkillFrontmatter(ent.Name(), string(raw))
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, SkillMetadata{
				Name:        name,
				Description: desc,
				Source:      "repo",
				Path:        skillPath,
			})
		}
	}
	if entries, err := fs.ReadDir(fabricassets.BuiltinSkillsFS, ".agents/skills"); err == nil {
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			embedPath := path.Join(".agents/skills", ent.Name(), "SKILL.md")
			raw, rErr := fs.ReadFile(fabricassets.BuiltinSkillsFS, embedPath)
			if rErr != nil || len(raw) == 0 || len(raw) > maxSkillBodyByte {
				continue
			}
			name, desc := parseSkillFrontmatter(ent.Name(), string(raw))
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, SkillMetadata{
				Name:        name,
				Description: desc,
				Source:      "built-in",
				embedPath:   embedPath,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func parseSkillFrontmatter(fallbackName, content string) (string, string) {
	name := fallbackName
	desc := ""
	norm := strings.ReplaceAll(content, "\r\n", "\n")
	if strings.HasPrefix(norm, "---\n") {
		rest := norm[4:]
		if end := strings.Index(rest, "\n---"); end >= 0 {
			fm := rest[:end]
			for _, line := range strings.Split(fm, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "name:") {
					val := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "name:")), `"'`)
					if val != "" {
						name = val
					}
				} else if strings.HasPrefix(line, "description:") {
					val := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "description:")), `"'`)
					if val != "" {
						desc = val
					}
				}
			}
		}
	}
	if desc == "" {
		desc = "Engineering workflow skill: " + name
	}
	return name, desc
}

func loadSkillBody(skills []SkillMetadata, name string) (string, bool) {
	q := strings.TrimSpace(name)
	for _, sk := range skills {
		if strings.EqualFold(sk.Name, q) {
			if sk.Path != "" {
				raw, err := os.ReadFile(sk.Path)
				if err == nil {
					return string(raw), true
				}
			}
			if sk.embedPath != "" {
				raw, err := fs.ReadFile(fabricassets.BuiltinSkillsFS, sk.embedPath)
				if err == nil {
					return string(raw), true
				}
			}
			return "", false
		}
	}
	return "", false
}

func defaultSkillSearchDirs(repoRoot, workDir string, extra []string) []string {
	var dirs []string
	if workDir != "" {
		dirs = append(dirs, filepath.Join(workDir, ".agents", "skills"))
	}
	if repoRoot != "" && filepath.Clean(repoRoot) != filepath.Clean(workDir) {
		dirs = append(dirs, filepath.Join(repoRoot, ".agents", "skills"))
	}
	dirs = append(dirs, extra...)
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		dirs = append(dirs,
			filepath.Join(exeDir, ".agents", "skills"),
			filepath.Join(filepath.Dir(exeDir), ".agents", "skills"),
		)
	}
	return dirs
}

// loadRepoGuidance reads repository-level AGENTS.md and CLAUDE.md files cleanly,
// bounded to 16 KiB total and wrapped for untrusted-context separation.
func loadRepoGuidance(dirs ...string) string {
	seen := map[string]bool{}
	var parts []string
	used := 0
	for _, d := range dirs {
		if d == "" {
			continue
		}
		cleanDir := filepath.Clean(d)
		if seen[cleanDir] {
			continue
		}
		seen[cleanDir] = true
		for _, fname := range []string{"AGENTS.md", "CLAUDE.md"} {
			p := filepath.Join(cleanDir, fname)
			b, err := os.ReadFile(p)
			if err != nil || len(b) == 0 {
				continue
			}
			txt := strings.TrimSpace(string(b))
			avail := maxGuidanceBytes - used
			if avail <= 256 {
				return strings.Join(parts, "\n\n")
			}
			if len(txt) > avail {
				txt = txt[:avail] + "\n... [truncated]"
			}
			used += len(txt)
			parts = append(parts, fenceUntrustedRepoGuidance(fname, txt))
		}
	}
	return strings.Join(parts, "\n\n")
}
