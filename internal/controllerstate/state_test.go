package controllerstate

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"harness.local/engorch/internal/repository"
)

func fixtureIdentity(root, name string) repository.Identity {
	return repository.Identity{
		Version: 1, Name: name, Root: root, CommonDir: filepath.Join(root, ".git"),
		ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40),
	}
}

func TestExternalNamespaceIsStableAcrossCommitsAndIdentityBound(t *testing.T) {
	base := t.TempDir()
	repositoryRoot := filepath.Join(base, "source")
	stateRoot := filepath.Join(base, "state")
	identity := fixtureIdentity(repositoryRoot, "fixture")
	paths, err := Resolve(stateRoot, identity)
	if err != nil || !paths.External || paths.Root == stateRoot || filepath.Dir(paths.Runs) != paths.Root {
		t.Fatal("external controller paths not resolved", paths, err)
	}
	advanced := identity
	advanced.Commit = strings.Repeat("c", 40)
	advanced.Tree = strings.Repeat("d", 40)
	again, err := Resolve(stateRoot, advanced)
	if err != nil || again.Root != paths.Root {
		t.Fatal("HEAD advance changed stable controller namespace", again, err)
	}
	if err := Initialize(paths, identity); err != nil {
		t.Fatal(err)
	}
	if err := Validate(paths, advanced); err != nil {
		t.Fatal("stable identity marker rejected after HEAD advance", err)
	}
	foreign := fixtureIdentity(filepath.Join(base, "other"), "fixture")
	if err := Validate(paths, foreign); err == nil {
		t.Fatal("foreign repository accepted existing controller namespace")
	}
	if err := Initialize(paths, foreign); err == nil {
		t.Fatal("foreign repository initialized another repository's paths")
	}
	if _, err := os.Stat(filepath.Join(paths.Root, markerName)); err != nil {
		t.Fatal("repository marker missing", err)
	}
}

func TestResolveRejectsUnsafeRootsAndPreservesLegacyPath(t *testing.T) {
	base := t.TempDir()
	repositoryRoot := filepath.Join(base, "source")
	identity := fixtureIdentity(repositoryRoot, "fixture")
	legacy, err := Resolve("", identity)
	if err != nil || legacy.External || legacy.Root != filepath.Join(repositoryRoot, ".harness") {
		t.Fatal("legacy controller path changed", legacy, err)
	}
	for _, unsafe := range []string{
		"relative-state",
		repositoryRoot,
		filepath.Join(repositoryRoot, "state"),
		base,
		filepath.VolumeName(repositoryRoot) + string(filepath.Separator),
	} {
		if _, err := Resolve(unsafe, identity); err == nil {
			t.Fatal("unsafe controller state root accepted", unsafe)
		}
	}
	if _, err := legacy.Run("../escape"); err == nil {
		t.Fatal("unsafe run ID accepted")
	}
}

func TestExternalNamespaceRejectsPathSubstitutionAndFilesystemAlias(t *testing.T) {
	base := t.TempDir()
	repositoryRoot := filepath.Join(base, "source")
	if err := os.Mkdir(repositoryRoot, 0700); err != nil {
		t.Fatal(err)
	}
	identity := fixtureIdentity(repositoryRoot, "fixture")
	paths, err := Resolve(filepath.Join(base, "state"), identity)
	if err != nil {
		t.Fatal(err)
	}
	changed := paths
	changed.Root = filepath.Join(base, "substituted")
	if err := Initialize(changed, identity); err == nil {
		t.Fatal("substituted public namespace accepted")
	}

	alias := filepath.Join(base, "alias")
	if err := os.Symlink(repositoryRoot, alias); err != nil {
		if runtime.GOOS != "windows" {
			t.Fatal("directory symlink unavailable:", err)
		}
		command := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `New-Item -ItemType Junction -Path $env:HARNESS_JUNCTION -Target $env:HARNESS_TARGET -ErrorAction Stop | Out-Null`)
		command.Env = append(os.Environ(), "HARNESS_JUNCTION="+alias, "HARNESS_TARGET="+repositoryRoot)
		if output, junctionErr := command.CombinedOutput(); junctionErr != nil {
			t.Fatal(junctionErr, string(output))
		}
	}
	aliased, err := Resolve(alias, identity)
	if err != nil {
		t.Fatal("pure resolution unexpectedly read symlink", err)
	}
	before, err := os.ReadDir(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := Initialize(aliased, identity); err == nil {
		t.Fatal("filesystem alias into repository accepted")
	}
	after, err := os.ReadDir(repositoryRoot)
	if err != nil || !reflect.DeepEqual(entryNames(before), entryNames(after)) {
		t.Fatal("alias rejection occurred after a repository write", err, entryNames(before), entryNames(after))
	}
}

func TestExternalNamespaceRejectsNestedJunctionAndHardlinkedMarker(t *testing.T) {
	base := t.TempDir()
	repositoryRoot := filepath.Join(base, "source")
	stateRoot := filepath.Join(base, "state")
	if err := os.Mkdir(repositoryRoot, 0700); err != nil {
		t.Fatal(err)
	}
	identity := fixtureIdentity(repositoryRoot, "fixture")
	if err := os.Mkdir(stateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(stateRoot, "repositories")
	if runtime.GOOS == "windows" {
		command := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `New-Item -ItemType Junction -Path $env:HARNESS_JUNCTION -Target $env:HARNESS_TARGET -ErrorAction Stop | Out-Null`)
		command.Env = append(os.Environ(), "HARNESS_JUNCTION="+junction, "HARNESS_TARGET="+repositoryRoot)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	} else if err := os.Symlink(repositoryRoot, junction); err != nil {
		t.Fatal(err)
	}
	aliased, err := Resolve(stateRoot, identity)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := Initialize(aliased, identity); err == nil {
		t.Fatal("nested repository namespace alias accepted")
	}
	after, err := os.ReadDir(repositoryRoot)
	if err != nil || !reflect.DeepEqual(entryNames(before), entryNames(after)) {
		t.Fatal("nested alias rejection occurred after a repository write", err, entryNames(before), entryNames(after))
	}
	if err := os.Remove(junction); err != nil {
		t.Fatal(err)
	}

	paths, err := Resolve(stateRoot, identity)
	if err != nil || Initialize(paths, identity) != nil {
		t.Fatal("ordinary namespace initialization failed", err)
	}
	markerPath := filepath.Join(paths.Root, markerName)
	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	externalMarker := filepath.Join(base, "external-marker.json")
	if err := os.WriteFile(externalMarker, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(externalMarker, markerPath); err != nil {
		t.Fatal(err)
	}
	if err := Validate(paths, identity); err == nil {
		t.Fatal("hardlinked repository marker accepted")
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names
}
