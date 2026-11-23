package agentcontext

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"go.yaml.in/yaml/v3"
)

const maxFiles = 96
const maxFileBytes = 16 << 10
const maxBundleBytes = 512 << 10
const maxPromptBytes = 96 << 10

var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Document retains complete UTF-8 instructions from one regular committed blob.
type Document struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

// Skill retains standard discovery metadata and the exact source document.
// Roles filters discovery only; it cannot grant a role any capability.
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Roles       []string `json:"roles"`
	Document    Document `json:"document"`
}

// Bundle is immutable run input, including instructions not yet model-visible.
// ID binds the inventory to the exact source commit and repository identity.
type Bundle struct {
	Version      int        `json:"version"`
	SourceID     string     `json:"source_id"`
	SourceCommit string     `json:"source_commit"`
	Instructions []Document `json:"instructions"`
	Skills       []Skill    `json:"skills"`
}

// Metadata is the cheap discovery view. It excludes skill bodies.
type Metadata struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Path        string   `json:"path"`
	SHA256      string   `json:"sha256"`
	Roles       []string `json:"roles"`
}

// Selection records exact role, task scope and selected skill content.
// The surrounding invocation binds candidate identity and this complete input.
type Selection struct {
	Version         int        `json:"version"`
	BundleID        string     `json:"bundle_id"`
	SourceCommit    string     `json:"source_commit"`
	Role            string     `json:"role"`
	ScopePaths      []string   `json:"scope_paths"`
	Instructions    []Document `json:"instructions"`
	AvailableSkills []Metadata `json:"available_skills"`
	SelectedSkills  []Skill    `json:"selected_skills"`
}

// Capture reads a bounded inventory from the committed source, never the live
// worktree. Unsupported instruction leaves and malformed skills fail explicitly
// before dispatch; absence of guidance is a valid empty inventory.
func Capture(ctx context.Context, source repository.Identity) (*Bundle, error) {
	id, err := source.ID()
	if err != nil {
		return nil, err
	}
	b := &Bundle{Version: 1, SourceID: id, SourceCommit: source.Commit, Instructions: []Document{}, Skills: []Skill{}}
	var entries []repository.SourceEntry
	err = repository.VisitSource(ctx, source, func(e repository.SourceEntry) error {
		parts := strings.Split(e.Path, "/")
		isSkill := len(parts) == 4 && parts[0] == ".agents" && parts[1] == "skills" && parts[3] == "SKILL.md"
		if path.Base(e.Path) != "AGENTS.md" && !isSkill {
			return nil
		}
		if e.Kind != "file" || e.Mode != "100644" && e.Mode != "100755" {
			return fmt.Errorf("agent context requires a regular committed file: %s", e.Path)
		}
		entries = append(entries, e)
		if len(entries) > maxFiles {
			return errors.New("agent context inventory exceeds 96 files")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return b, nil
	}
	var current bytes.Buffer
	total := 0
	err = repository.CopySelectedSourceBatch(ctx, source, entries, func(e repository.SourceEntry, size int64) (io.WriteCloser, error) {
		if size > maxFileBytes || size < 1 || total+int(size) > maxBundleBytes {
			return nil, fmt.Errorf("agent context size bound exceeded at %s", e.Path)
		}
		current.Reset()
		total += int(size)
		return bufferCloser{&current}, nil
	}, func(e repository.SourceEntry, digest *repository.SourceDigest) error {
		if digest == nil || !utf8.Valid(current.Bytes()) {
			return fmt.Errorf("agent context must contain complete UTF-8: %s", e.Path)
		}
		d := Document{e.Path, digest.SHA256, current.String()}
		if path.Base(e.Path) == "AGENTS.md" {
			b.Instructions = append(b.Instructions, d)
			return nil
		}
		s, err := parseSkill(d)
		if err != nil {
			return fmt.Errorf("skill %s: %w", e.Path, err)
		}
		b.Skills = append(b.Skills, s)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(b.Instructions, func(i, j int) bool { return b.Instructions[i].Path < b.Instructions[j].Path })
	sort.Slice(b.Skills, func(i, j int) bool { return b.Skills[i].Name < b.Skills[j].Name })
	return b, b.Validate()
}

type bufferCloser struct{ *bytes.Buffer }

// Close satisfies the source reader contract; the buffer owns no external resource.
func (bufferCloser) Close() error { return nil }

func parseSkill(d Document) (Skill, error) {
	text := strings.ReplaceAll(d.Content, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return Skill{}, errors.New("YAML frontmatter required")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return Skill{}, errors.New("unclosed YAML frontmatter")
	}
	var fields struct {
		Name        string            `yaml:"name"`
		Description string            `yaml:"description"`
		Metadata    map[string]string `yaml:"metadata"`
	}
	if err := yaml.Unmarshal([]byte(text[4:4+end]), &fields); err != nil {
		return Skill{}, errors.New("invalid skill frontmatter")
	}
	if !ValidName(fields.Name) || fields.Name != path.Base(path.Dir(d.Path)) || strings.TrimSpace(fields.Description) == "" || len(fields.Description) > 1024 {
		return Skill{}, errors.New("invalid skill name or description")
	}
	roles := []string{"planner", "explorer", "writer", "fixer", "reviewer"}
	if value, ok := fields.Metadata["fabric.roles"]; ok {
		roles = nil
		seen := map[string]bool{}
		for _, r := range strings.Split(value, ",") {
			r = strings.TrimSpace(r)
			if !validRole(r) || seen[r] {
				return Skill{}, errors.New("invalid skill role metadata")
			}
			seen[r] = true
			roles = append(roles, r)
		}
	}
	sort.Strings(roles)
	return Skill{fields.Name, fields.Description, roles, d}, nil
}

// ValidName accepts the bounded Agent Skills name form used by task references.
func ValidName(name string) bool { return len(name) <= 64 && skillName.MatchString(name) }
func validRole(r string) bool {
	return r == "planner" || r == "explorer" || r == "writer" || r == "fixer" || r == "reviewer"
}
func permits(s Skill, role string) bool {
	for _, r := range s.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Validate checks retained content and metadata without reopening any source.
func (b Bundle) Validate() error {
	if b.Version != 1 || safepath.RequireDigest(b.SourceID) != nil || (len(b.SourceCommit) != 40 && len(b.SourceCommit) != 64) || strings.Trim(b.SourceCommit, "0123456789abcdef") != "" || len(b.Instructions)+len(b.Skills) > maxFiles || len(b.Skills) > 32 {
		return errors.New("invalid agent context bundle")
	}
	total := 0
	seen := map[string]bool{}
	check := func(d Document) error {
		if safepath.Relative(d.Path) != nil || seen[d.Path] || len(d.Content) == 0 || len(d.Content) > maxFileBytes || !utf8.ValidString(d.Content) {
			return errors.New("invalid agent context document")
		}
		seen[d.Path] = true
		total += len(d.Content)
		h := sha256.Sum256([]byte(d.Content))
		if hex.EncodeToString(h[:]) != d.SHA256 {
			return errors.New("agent context document hash mismatch")
		}
		return nil
	}
	previous := ""
	for _, d := range b.Instructions {
		if path.Base(d.Path) != "AGENTS.md" || d.Path <= previous {
			return errors.New("invalid instruction ordering")
		}
		previous = d.Path
		if err := check(d); err != nil {
			return err
		}
	}
	previous = ""
	for _, s := range b.Skills {
		if s.Document.Path != ".agents/skills/"+s.Name+"/SKILL.md" || s.Name <= previous {
			return errors.New("invalid skill ordering")
		}
		previous = s.Name
		if err := check(s.Document); err != nil {
			return err
		}
		parsed, err := parseSkill(s.Document)
		if err != nil {
			return err
		}
		if parsed.Name != s.Name || parsed.Description != s.Description || strings.Join(parsed.Roles, ",") != strings.Join(s.Roles, ",") {
			return errors.New("skill metadata differs from bound document")
		}
	}
	if total > maxBundleBytes {
		return errors.New("agent context bundle exceeds byte limit")
	}
	return nil
}

// ID names the complete immutable inventory, rather than just selected bodies.
func (b Bundle) ID() (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash("fabric.agent-context-bundle.v1", b)
}

// Resolve filters guidance by task scope, preserves parent-before-child order,
// and includes bodies only for explicitly selected role-permitted skills.
// Root scope includes only root guidance; it never loads every nested rule.
func (b Bundle) Resolve(role string, scopes, names []string) (Selection, error) {
	id, err := b.ID()
	if err != nil {
		return Selection{}, err
	}
	if !validRole(role) || len(scopes) > 2048 || len(names) > 4 {
		return Selection{}, errors.New("invalid agent context selection")
	}
	paths := map[string]bool{}
	for _, p := range scopes {
		if p != "." && safepath.Relative(p) != nil {
			return Selection{}, errors.New("invalid agent context scope")
		}
		paths[p] = true
	}
	keys := make([]string, 0, len(paths))
	for p := range paths {
		keys = append(keys, p)
	}
	if len(keys) > 512 {
		return Selection{}, errors.New("agent context exceeds 512 unique scope paths")
	}
	sort.Strings(keys)
	out := Selection{1, id, b.SourceCommit, role, keys, []Document{}, []Metadata{}, []Skill{}}
	for _, d := range b.Instructions {
		dir := path.Dir(d.Path)
		applicable := dir == "."
		if role != "planner" {
			for _, p := range keys {
				if p == dir || strings.HasPrefix(p, dir+"/") || p != "." && strings.HasPrefix(dir, p+"/") {
					applicable = true
				}
			}
		}
		if applicable {
			out.Instructions = append(out.Instructions, d)
		}
	}
	sort.SliceStable(out.Instructions, func(i, j int) bool {
		a, c := out.Instructions[i].Path, out.Instructions[j].Path
		da, dc := strings.Count(a, "/"), strings.Count(c, "/")
		if da != dc {
			return da < dc
		}
		return a < c
	})
	selected := map[string]bool{}
	for _, n := range names {
		if !ValidName(n) || selected[n] {
			return Selection{}, errors.New("invalid or duplicate selected skill")
		}
		selected[n] = true
	}
	for _, s := range b.Skills {
		if permits(s, role) || role == "planner" {
			out.AvailableSkills = append(out.AvailableSkills, Metadata{s.Name, s.Description, s.Document.Path, s.Document.SHA256, s.Roles})
		}
		if selected[s.Name] && permits(s, role) {
			out.SelectedSkills = append(out.SelectedSkills, s)
			delete(selected, s.Name)
		}
	}
	if len(selected) != 0 {
		return Selection{}, errors.New("selected skill is missing or unavailable for role")
	}
	raw, err := canonical.Bytes(out)
	if err != nil {
		return Selection{}, err
	}
	if len(raw) > maxPromptBytes {
		return Selection{}, errors.New("agent context selection exceeds 96 KiB")
	}
	return out, nil
}
