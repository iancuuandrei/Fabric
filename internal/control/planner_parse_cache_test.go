package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
)

func TestPlannerParseCachePolicyIsOptInAndContextBound(t *testing.T) {
	legacy := ExecutionPolicy{Mode: "autonomous-v1"}
	legacyBytes, err := canonical.Bytes(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacyBytes), "planner_parse_cache_version") {
		t.Fatal("zero parse-cache policy changed legacy canonical bytes")
	}
	if err := legacy.Validate(); err != nil {
		t.Fatal(err)
	}

	base := legacy
	base.PlannerContext = plannerContextGoSourceV2
	base.PlannerContextRIExecutable = filepath.Join(t.TempDir(), "engorch-ri.exe")
	base.PlannerContextRIExecutableSHA256 = strings.Repeat("a", 64)
	if err := base.Validate(); err != nil {
		t.Fatalf("v2 without cache opt-in rejected: %v", err)
	}
	opted := base
	opted.PlannerParseCacheVersion = 1
	if err := opted.Validate(); err != nil {
		t.Fatalf("v2 cache opt-in rejected: %v", err)
	}
	contract := opted
	contract.PlannerContext = plannerContextGoContractV1
	if err := contract.Validate(); err != nil {
		t.Fatalf("contract cache opt-in rejected: %v", err)
	}
	receiverAware := opted
	receiverAware.PlannerContext = plannerContextGoContractV2
	if err := receiverAware.Validate(); err != nil {
		t.Fatalf("receiver-aware contract cache opt-in rejected: %v", err)
	}
	contractV3 := opted
	contractV3.PlannerContext = plannerContextGoContractV3
	if err := contractV3.Validate(); err != nil {
		t.Fatalf("contract v3 cache opt-in rejected: %v", err)
	}
	for name, candidate := range map[string]ExecutionPolicy{
		"unknown version": func() ExecutionPolicy { p := opted; p.PlannerParseCacheVersion = 2; return p }(),
		"v1 context":      func() ExecutionPolicy { p := opted; p.PlannerContext = plannerContextGoSourceV1; return p }(),
		"no context":      func() ExecutionPolicy { p := opted; p.PlannerContext = ""; return p }(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid parse-cache policy accepted")
			}
		})
	}
}

func TestPlannerParseCachePathStableAcrossCommitsAndScopedByCheckoutAndProducer(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	identity := repository.Identity{Version: 1, Name: "fixture", Root: root, CommonDir: filepath.Join(root, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	userCache := filepath.Join(t.TempDir(), "user-cache")
	producer := strings.Repeat("c", 64)
	first, err := plannerParseCachePath(userCache, identity, producer)
	if err != nil {
		t.Fatal(err)
	}
	changedCommit := identity
	changedCommit.Commit = strings.Repeat("d", 40)
	changedCommit.Tree = strings.Repeat("e", 40)
	second, err := plannerParseCachePath(userCache, changedCommit, producer)
	if err != nil || first != second {
		t.Fatalf("commit/tree unexpectedly changed local cache namespace: %q %q err=%v", first, second, err)
	}
	changedCheckout := identity
	changedCheckout.Root = filepath.Join(t.TempDir(), "repo")
	changedCheckout.CommonDir = filepath.Join(changedCheckout.Root, ".git")
	third, err := plannerParseCachePath(userCache, changedCheckout, producer)
	if err != nil || third == first {
		t.Fatalf("different checkout reused cache namespace: %q %q err=%v", first, third, err)
	}
	fourth, err := plannerParseCachePath(userCache, identity, strings.Repeat("f", 64))
	if err != nil || fourth == first {
		t.Fatalf("different parser producer reused cache namespace: %q %q err=%v", first, fourth, err)
	}
	if pathContains(identity.Root, first) || pathContains(identity.CommonDir, first) || !pathContains(userCache, first) {
		t.Fatalf("cache path not correctly isolated: %q", first)
	}
}

func TestPlannerParseCacheRejectsLinkedAncestorBeforeCreatingLeaf(t *testing.T) {
	userCache := t.TempDir()
	redirect := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", userCache)
	} else {
		t.Setenv("XDG_CACHE_HOME", userCache)
	}
	linked := filepath.Join(userCache, "Fabric")
	if err := os.Symlink(redirect, linked); err != nil {
		t.Skipf("platform cannot create a directory symlink for this test: %v", err)
	}
	root := filepath.Join(t.TempDir(), "repo")
	identity := repository.Identity{Version: 1, Name: "fixture", Root: root, CommonDir: filepath.Join(root, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	if _, err := ensurePlannerParseCacheDir(identity, strings.Repeat("c", 64)); err == nil {
		t.Fatal("planner parse cache followed a linked user-cache ancestor")
	}
	entries, err := os.ReadDir(redirect)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("planner cache created directories through the linked ancestor: %v", entries)
	}
}

func TestPlannerParseCacheCreatesMissingUserCacheDirectorySafely(t *testing.T) {
	userCache := filepath.Join(t.TempDir(), "not-created", "yet", "cache")
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", userCache)
	} else {
		t.Setenv("XDG_CACHE_HOME", userCache)
	}
	root := filepath.Join(t.TempDir(), "repo")
	identity := repository.Identity{Version: 1, Name: "fixture", Root: root, CommonDir: filepath.Join(root, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	cacheDir, err := ensurePlannerParseCacheDir(identity, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if !pathContains(userCache, cacheDir) {
		t.Fatalf("created cache is not beneath configured user cache: %q", cacheDir)
	}
	if _, err := os.Stat(cacheDir); err != nil {
		t.Fatalf("missing user cache was not safely created: %v", err)
	}
}

func TestPlannerParseCacheOptInPreservesPlannerIdentityAndReplayDoesNotRequery(t *testing.T) {
	for _, mode := range []string{plannerContextGoSourceV2, plannerContextGoContractV1, plannerContextGoContractV2} {
		t.Run(mode, func(t *testing.T) { testPlannerParseCacheIdentity(t, mode) })
	}
}

func testPlannerParseCacheIdentity(t *testing.T, mode string) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("ENGORCH_RI_BINARY is required for local RI fixture")
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	producer := hex.EncodeToString(digest[:])
	userCache := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", userCache)
	} else {
		t.Setenv("XDG_CACHE_HOME", userCache)
	}

	c := autonomousCreation(t, 0)
	c.Objective = "Review the bounded parser cache fixture and generated helpers"
	c.Execution.PlannerContext = mode
	c.Execution.PlannerContextRIExecutable = filepath.Clean(executable)
	c.Execution.PlannerContextRIExecutableSHA256 = producer
	c.Execution.PlannerParseCacheVersion = 1
	root := c.Repository.Root
	autonomousGitInit(t, root)
	files := map[string]string{
		"go.mod":            "module example.test/cache\n\ngo 1.25\n",
		"pkg/value.go":      "package pkg\n\nfunc Value() int { return 7 }\n",
		"pkg/value_test.go": "package pkg\n\nfunc ExampleValue() { _ = Value() }\n",
	}
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "Go parse-cache fixture"}} {
		if out, err := autonomousGitCmd(t, root, args); err != nil {
			t.Fatal(err, string(out))
		}
	}
	c.Repository, err = repository.Discover(context.Background(), root, c.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(t.TempDir(), "cold.jsonl")
	if err := Append(firstPath, "run.created", c); err != nil {
		t.Fatal(err)
	}
	cold, err := AdmitPlannerGoContext(context.Background(), firstPath)
	if err != nil {
		t.Fatal(err)
	}
	if cold.Graph == nil || cold.Unavailable != "" || mode == plannerContextGoSourceV2 && cold.Context == nil || (mode == plannerContextGoContractV1 || mode == plannerContextGoContractV2) && cold.ContractContext == nil {
		t.Fatal("cache-enabled admission did not produce the selected evidence")
	}
	if mode == plannerContextGoContractV2 && (cold.Version != 5 || cold.CorpusSelectionVersion != ri.GoCorpusSelectionReceiverAwareV2) {
		t.Fatalf("receiver-aware cache admission lost selection binding: version=%d selection=%d", cold.Version, cold.CorpusSelectionVersion)
	}
	cacheDir, err := ensurePlannerParseCacheDir(c.Repository, producer)
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(cacheDir); err != nil || len(entries) == 0 {
		t.Fatalf("RI cache was not populated: entries=%d err=%v", len(entries), err)
	}

	uncachedCreation := c
	uncachedCreation.Execution = &ExecutionPolicy{}
	*uncachedCreation.Execution = *c.Execution
	uncachedCreation.Execution.PlannerParseCacheVersion = 0
	uncachedPath := filepath.Join(t.TempDir(), "uncached.jsonl")
	if err := Append(uncachedPath, "run.created", uncachedCreation); err != nil {
		t.Fatal(err)
	}
	uncached, err := AdmitPlannerGoContext(context.Background(), uncachedPath)
	if err != nil {
		t.Fatal(err)
	}

	secondPath := filepath.Join(t.TempDir(), "warm.jsonl")
	if err := Append(secondPath, "run.created", c); err != nil {
		t.Fatal(err)
	}
	warm, err := AdmitPlannerGoContext(context.Background(), secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if uncached.RecordID != cold.RecordID || cold.RecordID != warm.RecordID || uncached.Graph.Digest != cold.Graph.Digest || cold.Graph.Digest != warm.Graph.Digest || plannerCacheEvidenceDigest(t, uncached) != plannerCacheEvidenceDigest(t, cold) || plannerCacheEvidenceDigest(t, cold) != plannerCacheEvidenceDigest(t, warm) {
		t.Fatal("cache mode changed durable planner evidence identity")
	}
	coldSnapshot, err := Inspect(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	warmSnapshot, err := Inspect(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	uncachedSnapshot, err := Inspect(uncachedPath)
	if err != nil {
		t.Fatal(err)
	}
	coldInvocation, err := plannerInvocationForSnapshot(coldSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	warmInvocation, err := plannerInvocationForSnapshot(warmSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	uncachedInvocation, err := plannerInvocationForSnapshot(uncachedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if uncachedInvocation.Input != coldInvocation.Input || coldInvocation.Input != warmInvocation.Input {
		t.Fatal("cache mode changed planner prompt bytes")
	}
	recordBytes, err := json.Marshal(cold)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(recordBytes), cacheDir) {
		t.Fatal("local cache path leaked into the durable planner context")
	}
	for _, file := range cold.Graph.Files {
		if file.Facts.Cache != "" || file.Facts.ParseCount != 0 {
			t.Fatalf("durable graph retained cache observations for %s", file.Facts.Path)
		}
	}

	// Removing local cache entries after admission must not cause replay to run
	// RI again. The immutable admitted record is sufficient for replay.
	if err := os.RemoveAll(cacheDir); err != nil {
		t.Fatal(err)
	}
	beforeReplay, err := json.Marshal(cold)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := AdmitPlannerGoContext(context.Background(), firstPath)
	if err != nil {
		t.Fatal(err)
	}
	afterReplay, err := json.Marshal(replayed)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeReplay) != string(afterReplay) {
		t.Fatal("planner Go context replay changed the admitted record")
	}
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Fatalf("planner context replay re-created or inspected the parse cache: %v", err)
	}
}

func plannerCacheEvidenceDigest(t *testing.T, record PlannerGoContextRecord) string {
	t.Helper()
	if record.Context != nil {
		return record.Context.Digest
	}
	if record.ContractContext != nil {
		return record.ContractContext.Digest
	}
	t.Fatal("planner cache fixture has no context evidence")
	return ""
}
