package control

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/safepath"
)

const (
	plannerParseCachePathDomain   = "harness.control.planner-go-parse-cache-checkout.v1"
	candidateFactsCachePathDomain = "harness.control.candidate-go-facts-cache-checkout.v1"
)

type plannerParseCacheCheckout struct {
	Name         string `json:"name"`
	Root         string `json:"root"`
	CommonDir    string `json:"common_dir"`
	ObjectFormat string `json:"object_format"`
}

// plannerParseCachePath derives a stable cache leaf without binding cache
// observations to a particular commit or tree. The parser producer digest is
// a separate path component, while source and producer hashes remain in each
// RI cache key.
func plannerParseCachePath(userCacheDir string, identity repository.Identity, producerSHA256 string) (string, error) {
	if err := identity.Validate(); err != nil {
		return "", err
	}
	if safepath.RequireDigest(producerSHA256) != nil || !filepath.IsAbs(userCacheDir) || filepath.Clean(userCacheDir) != userCacheDir {
		return "", errors.New("invalid planner parse-cache root or producer")
	}
	root := plannerParseCacheCheckout{Name: identity.Name, Root: filepath.Clean(identity.Root), CommonDir: filepath.Clean(identity.CommonDir), ObjectFormat: identity.ObjectFormat}
	checkoutID, err := canonical.Hash(plannerParseCachePathDomain, root)
	if err != nil {
		return "", err
	}
	return filepath.Join(userCacheDir, "Fabric", "ri", "go-file-facts", checkoutID, producerSHA256), nil
}

// ensurePlannerParseCacheDir creates and validates the per-checkout,
// per-producer cache leaf. Its absolute path is local configuration only and
// is never serialized into a run record or model prompt.
func ensurePlannerParseCacheDir(identity repository.Identity, producerSHA256 string) (string, error) {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	userCacheDir = filepath.Clean(userCacheDir)
	if !filepath.IsAbs(userCacheDir) || userCacheDir == filepath.VolumeName(userCacheDir)+string(filepath.Separator) {
		return "", errors.New("planner parse-cache requires a non-root user cache directory")
	}
	cacheDir, err := plannerParseCachePath(userCacheDir, identity, producerSHA256)
	if err != nil {
		return "", err
	}
	if err := rejectParseCacheRepositoryOverlap(cacheDir, identity); err != nil {
		return "", err
	}
	// The platform's user cache directory may itself be absent (for example,
	// XDG_CACHE_HOME or ~/.cache). Find and validate the nearest existing
	// ancestor, then create every component through os.Root without traversing
	// any link/reparse point.
	if err := ensurePlannerParseCacheDirectories(userCacheDir, cacheDir); err != nil {
		return "", err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(cacheDir, 0700); err != nil {
			return "", err
		}
	}
	if err := safepath.Directory(cacheDir); err != nil {
		return "", err
	}
	return cacheDir, nil
}

func ensurePlannerParseCacheDirectories(userCacheDir, cacheDir string) error {
	if !pathContains(userCacheDir, cacheDir) {
		return errors.New("planner parse-cache leaf is outside user cache")
	}
	ancestor := userCacheDir
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return err
		}
		ancestor = parent
	}
	if err := safepath.Directory(ancestor); err != nil {
		return err
	}
	relativeLeaf, err := filepath.Rel(ancestor, cacheDir)
	if err != nil || safepath.Relative(filepath.ToSlash(relativeLeaf)) != nil {
		return errors.New("invalid planner parse-cache leaf")
	}
	return safepath.EnsureDirectory(ancestor, filepath.ToSlash(relativeLeaf))
}

func rejectParseCacheRepositoryOverlap(cacheDir string, identity repository.Identity) error {
	for _, repoPath := range []string{identity.Root, identity.CommonDir} {
		if !filepath.IsAbs(repoPath) {
			return errors.New("planner parse-cache repository path is not absolute")
		}
		if pathContains(repoPath, cacheDir) || pathContains(cacheDir, repoPath) {
			return errors.New("planner parse-cache must remain outside the repository and Git metadata")
		}
	}
	return nil
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		rel = strings.ToLower(rel)
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// ensureCandidateFactsCacheDir creates a separate local cache namespace for
// candidate review facts. It shares no entries with planner corpus caching.
func ensureCandidateFactsCacheDir(identity repository.Identity, producerSHA256 string) (string, error) {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	userCacheDir = filepath.Clean(userCacheDir)
	if !filepath.IsAbs(userCacheDir) || userCacheDir == filepath.VolumeName(userCacheDir)+string(filepath.Separator) {
		return "", errors.New("candidate facts cache requires a non-root user cache directory")
	}
	if err := identity.Validate(); err != nil || safepath.RequireDigest(producerSHA256) != nil {
		return "", errors.New("invalid candidate facts cache identity or producer")
	}
	root := plannerParseCacheCheckout{Name: identity.Name, Root: filepath.Clean(identity.Root), CommonDir: filepath.Clean(identity.CommonDir), ObjectFormat: identity.ObjectFormat}
	checkoutID, err := canonical.Hash(candidateFactsCachePathDomain, root)
	if err != nil {
		return "", err
	}
	cacheDir := filepath.Join(userCacheDir, "Fabric", "ri", "go-candidate-file-facts", checkoutID, producerSHA256)
	if err := rejectParseCacheRepositoryOverlap(cacheDir, identity); err != nil {
		return "", err
	}
	if err := ensurePlannerParseCacheDirectories(userCacheDir, cacheDir); err != nil {
		return "", err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(cacheDir, 0700); err != nil {
			return "", err
		}
	}
	if err := safepath.Directory(cacheDir); err != nil {
		return "", err
	}
	return cacheDir, nil
}
