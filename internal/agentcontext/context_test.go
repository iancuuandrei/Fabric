package agentcontext

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/repository"
)

func document(name, body string) Document {
	sum := sha256.Sum256([]byte(body))
	return Document{name, hex.EncodeToString(sum[:]), body}
}
func bundleFixture(t *testing.T) Bundle {
	t.Helper()
	b := Bundle{Version: 1, SourceID: strings.Repeat("a", 64), SourceCommit: strings.Repeat("b", 40), Instructions: []Document{document("AGENTS.md", "root"), document("docs/AGENTS.md", "docs"), document("internal/AGENTS.md", "internal"), document("internal/control/AGENTS.md", "control")}}
	for _, v := range []struct{ name, roles string }{{"code-review", "reviewer"}, {"tdd", "writer,fixer"}} {
		d := document(".agents/skills/"+v.name+"/SKILL.md", "---\nname: "+v.name+"\ndescription: Workflow for "+v.name+".\nmetadata:\n  fabric.roles: \""+v.roles+"\"\n---\nprivate body for "+v.name+"\n")
		s, err := parseSkill(d)
		if err != nil {
			t.Fatal(err)
		}
		b.Skills = append(b.Skills, s)
	}
	return b
}

func TestAgentContextScopeAndProgressiveDisclosure(t *testing.T) {
	b := bundleFixture(t)
	planner, err := b.Resolve("planner", []string{"internal/control/x.go"}, nil)
	if err != nil || len(planner.Instructions) != 1 || len(planner.AvailableSkills) != 2 || len(planner.SelectedSkills) != 0 {
		t.Fatalf("planner selection: %+v %v", planner, err)
	}
	raw, _ := json.Marshal(planner)
	if strings.Contains(string(raw), "private body") {
		t.Fatal("unselected body leaked into metadata")
	}
	writer, err := b.Resolve("writer", []string{"internal/control/x.go"}, []string{"tdd"})
	if err != nil {
		t.Fatal(err)
	}
	if len(writer.Instructions) != 3 || writer.Instructions[0].Path != "AGENTS.md" || writer.Instructions[1].Path != "internal/AGENTS.md" || writer.Instructions[2].Path != "internal/control/AGENTS.md" {
		t.Fatalf("precedence/scope: %+v", writer.Instructions)
	}
	if len(writer.AvailableSkills) != 1 || len(writer.SelectedSkills) != 1 || writer.SelectedSkills[0].Name != "tdd" {
		t.Fatal("writer filtering")
	}
	root, err := b.Resolve("explorer", []string{"."}, nil)
	if err != nil || len(root.Instructions) != 1 {
		t.Fatal("root scope pulled nested rules", err)
	}
	near, err := b.Resolve("writer", []string{"internality/x.go"}, nil)
	if err != nil || len(near.Instructions) != 1 {
		t.Fatal("path prefix escaped directory boundary", err)
	}
	subtree, err := b.Resolve("writer", []string{"internal"}, nil)
	if err != nil || len(subtree.Instructions) != 3 {
		t.Fatal("explicit subtree lost contained guidance", err)
	}
}

func TestAgentContextRejectsCommittedSymlink(t *testing.T) {
	i := commitFixture(t, map[string]string{"AGENTS.md": "outside-target"})
	raw, err := exec.Command("git", "-C", i.Root, "rev-parse", "HEAD:AGENTS.md").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"update-index", "--cacheinfo", "120000," + strings.TrimSpace(string(raw)) + ",AGENTS.md"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "unsupported link"}} {
		if result, err := exec.Command("git", append([]string{"-C", i.Root}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(result))
		}
	}
	i, err = repository.Discover(context.Background(), i.Root, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(context.Background(), i); err == nil {
		t.Fatal("committed instruction symlink admitted")
	}
}

func TestAgentContextRejectsSelectionAndRetainedTampering(t *testing.T) {
	b := bundleFixture(t)
	for _, c := range []struct {
		role         string
		paths, names []string
	}{{"writer", nil, []string{"code-review"}}, {"writer", nil, []string{"missing"}}, {"writer", nil, []string{"tdd", "tdd"}}, {"writer", []string{"../outside"}, nil}, {"unknown", nil, nil}} {
		if _, err := b.Resolve(c.role, c.paths, c.names); err == nil {
			t.Fatalf("invalid selection admitted %+v", c)
		}
	}
	changed := bundleFixture(t)
	changed.Instructions[0].Content = "tampered"
	if changed.Validate() == nil {
		t.Fatal("content tampering admitted")
	}
	changed = bundleFixture(t)
	changed.Skills[0].Roles = []string{"writer"}
	if changed.Validate() == nil {
		t.Fatal("metadata substitution admitted")
	}
	first, _ := b.ID()
	b.Instructions[0] = document("AGENTS.md", "new committed rules")
	second, _ := b.ID()
	if first == second {
		t.Fatal("instruction change did not change identity")
	}
}

func commitFixture(t *testing.T, files map[string]string) repository.Identity {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		raw, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatal(err, string(raw))
		}
	}
	git("init", "-q")
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "source")
	i, err := repository.Discover(context.Background(), root, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestAgentContextCaptureAndOfflineReplay(t *testing.T) {
	i := commitFixture(t, map[string]string{"AGENTS.md": "committed instructions", "src/x.go": "package src\n", ".agents/skills/tdd/SKILL.md": "---\nname: tdd\ndescription: Discriminating regression work.\nmetadata:\n  fabric.roles: \"writer,fixer\"\n---\nexact committed skill\n"})
	if err := os.WriteFile(filepath.Join(i.Root, "AGENTS.md"), []byte("dirty instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := Capture(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	if b.Instructions[0].Content != "committed instructions" {
		t.Fatal("read dirty instructions")
	}
	before, err := b.Resolve("writer", []string{"src/x.go"}, []string{"tdd"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var restored Bundle
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(i.Root, ".git"), filepath.Join(i.Root, "offline-git")); err != nil {
		t.Fatal(err)
	}
	after, err := restored.Resolve("writer", []string{"src/x.go"}, []string{"tdd"})
	if err != nil {
		t.Fatal("replay touched live Git", err)
	}
	a, _ := json.Marshal(before)
	c, _ := json.Marshal(after)
	if string(a) != string(c) {
		t.Fatal("retained selection drift")
	}
}

func TestAgentContextRejectsMalformedAndOversizedCapture(t *testing.T) {
	for _, files := range []map[string]string{{".agents/skills/tdd/SKILL.md": "no frontmatter"}, {"AGENTS.md": strings.Repeat("x", maxFileBytes+1)}, {".agents/skills/tdd/SKILL.md": "---\nname: other\ndescription: mismatch\n---\nbody\n"}, {".agents/skills/tdd/SKILL.md": "---\nname: tdd\ndescription: duplicate roles\nmetadata:\n  fabric.roles: \"writer,writer\"\n---\nbody\n"}} {
		i := commitFixture(t, files)
		if _, err := Capture(context.Background(), i); err == nil {
			t.Fatal("invalid committed context admitted")
		}
	}
}

func TestRepositorySkillsCatalogValid(t *testing.T) {
	skillsRoot := filepath.Join("..", "..", ".agents", "skills")
	entries, err := os.ReadDir(skillsRoot)
	if err != nil {
		t.Fatalf("read %s: %v", skillsRoot, err)
	}
	b := Bundle{
		Version:      1,
		SourceID:     strings.Repeat("a", 64),
		SourceCommit: strings.Repeat("b", 40),
		Instructions: []Document{document("AGENTS.md", "root")},
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(skillsRoot, ent.Name(), "SKILL.md"))
		if err != nil {
			t.Fatalf("read skill %s: %v", ent.Name(), err)
		}
		text := strings.ReplaceAll(string(raw), "\r\n", "\n")
		if lines := strings.Count(text, "\n") + 1; lines > 500 {
			t.Fatalf("skill %s exceeds 500-line hygiene limit (%d lines)", ent.Name(), lines)
		}
		s, err := parseSkill(document(".agents/skills/"+ent.Name()+"/SKILL.md", string(raw)))
		if err != nil {
			t.Fatalf("skill %s failed parseSkill: %v", ent.Name(), err)
		}
		b.Skills = append(b.Skills, s)
	}
	if len(b.Skills) < 32 {
		t.Fatalf("expected at least 32 repository skills, got %d", len(b.Skills))
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("repository skills bundle invalid: %v", err)
	}
}
