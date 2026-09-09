// Package controllerstate derives the bounded durable controller namespace for
// one repository checkout. It does not provide a general configuration store.
package controllerstate

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/safepath"
)

const (
	markerName     = "repository.json"
	maxMarkerBytes = 64 << 10
)

// Paths are the controller-owned mutable paths for one repository checkout.
// Root is the repository namespace, not the operator-selected shared state root.
type Paths struct {
	Root         string
	Runs         string
	External     bool
	marker       []byte
	stateRoot    string
	protected    []string
	expectedRoot string
	expectedRuns string
}

type repositoryLocator struct {
	Version      int    `json:"version"`
	Name         string `json:"name"`
	Root         string `json:"root"`
	CommonDir    string `json:"common_dir"`
	ObjectFormat string `json:"object_format"`
}

func locator(identity repository.Identity) (repositoryLocator, error) {
	if err := identity.Validate(); err != nil {
		return repositoryLocator{}, err
	}
	return repositoryLocator{
		Version: identity.Version, Name: identity.Name,
		Root: filepath.Clean(identity.Root), CommonDir: filepath.Clean(identity.CommonDir),
		ObjectFormat: identity.ObjectFormat,
	}, nil
}

// Resolve derives paths without reading or writing the filesystem. An empty
// configuredRoot preserves the historical repository-local .harness layout.
// External namespaces use stable checkout identity and therefore remain
// discoverable after HEAD advances.
func Resolve(configuredRoot string, identity repository.Identity) (Paths, error) {
	stable, err := locator(identity)
	if err != nil {
		return Paths{}, err
	}
	if configuredRoot == "" {
		root := filepath.Join(stable.Root, ".harness")
		return Paths{Root: root, Runs: filepath.Join(root, "runs")}, nil
	}
	if !filepath.IsAbs(configuredRoot) || filepath.Clean(configuredRoot) != configuredRoot || isVolumeRoot(configuredRoot) {
		return Paths{}, errors.New("controller state root must be an absolute clean non-volume-root path")
	}
	for _, protected := range []string{stable.Root, stable.CommonDir} {
		if pathsOverlap(configuredRoot, protected) {
			return Paths{}, errors.New("controller state root overlaps repository or Git control paths")
		}
	}
	id, err := canonical.Hash("harness.controller-state-repository.v1", stable)
	if err != nil {
		return Paths{}, err
	}
	marker, err := markerBytes(identity)
	if err != nil {
		return Paths{}, err
	}
	root := filepath.Join(configuredRoot, "repositories", id)
	runs := filepath.Join(root, "runs")
	return Paths{Root: root, Runs: runs, External: true, marker: marker, stateRoot: configuredRoot, protected: []string{stable.Root, stable.CommonDir}, expectedRoot: root, expectedRuns: runs}, nil
}

// Run derives one run journal path after validating its lowercase digest name.
func (p Paths) Run(id string) (string, error) {
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		return "", errors.New("run ID must be 64 lowercase hex characters")
	}
	if !filepath.IsAbs(p.Runs) || filepath.Clean(p.Runs) != p.Runs {
		return "", errors.New("invalid controller runs path")
	}
	return filepath.Join(p.Runs, id+".jsonl"), nil
}

// Validate checks an existing external namespace marker without mutation.
// Legacy repository-local paths have no marker and remain compatible.
func Validate(paths Paths, identity repository.Identity) error {
	if !paths.External {
		return nil
	}
	want, err := markerBytes(identity)
	if err != nil {
		return err
	}
	if !bytes.Equal(paths.marker, want) {
		return errors.New("controller state paths belong to a different repository identity")
	}
	if paths.Root != paths.expectedRoot || paths.Runs != paths.expectedRuns {
		return errors.New("controller state path derivation mismatch")
	}
	if err := validateResolvedSeparation(paths); err != nil {
		return err
	}
	got, err := readMarker(filepath.Join(paths.Root, markerName))
	if err != nil {
		return errors.Join(errors.New("controller state repository marker unavailable"), err)
	}
	if !bytes.Equal(got, want) {
		return errors.New("controller state repository identity mismatch")
	}
	return nil
}

// Initialize creates a new external namespace marker atomically or validates
// an existing one. It is idempotent for the same stable repository identity.
func Initialize(paths Paths, identity repository.Identity) error {
	if !paths.External {
		return nil
	}
	want, err := markerBytes(identity)
	if err != nil {
		return err
	}
	if !bytes.Equal(paths.marker, want) {
		return errors.New("controller state paths belong to a different repository identity")
	}
	if paths.Root != paths.expectedRoot || paths.Runs != paths.expectedRuns {
		return errors.New("controller state path derivation mismatch")
	}
	if err := validateResolvedSeparation(paths); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.Root, 0700); err != nil {
		return err
	}
	if err := validateResolvedSeparation(paths); err != nil {
		return err
	}
	markerPath := filepath.Join(paths.Root, markerName)
	if got, readErr := readMarker(markerPath); readErr == nil {
		if !bytes.Equal(got, want) {
			return errors.New("controller state repository identity mismatch")
		}
		return nil
	} else if !os.IsNotExist(readErr) {
		return readErr
	}
	marker, err := os.OpenFile(markerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return Validate(paths, identity)
		}
		return err
	}
	if err = marker.Chmod(0600); err == nil {
		_, err = marker.Write(want)
	}
	if err == nil {
		err = marker.Sync()
	}
	return errors.Join(err, marker.Close())
}

func markerBytes(identity repository.Identity) ([]byte, error) {
	stable, err := locator(identity)
	if err != nil {
		return nil, err
	}
	encoded, err := canonical.Bytes(stable)
	if err != nil {
		return nil, err
	}
	if len(encoded) == 0 || len(encoded) > maxMarkerBytes {
		return nil, errors.New("controller state repository marker exceeds size bound")
	}
	return encoded, nil
}

func isVolumeRoot(path string) bool {
	volume := filepath.VolumeName(path)
	remainder := strings.TrimPrefix(path, volume)
	return remainder == string(filepath.Separator)
}

func pathsOverlap(a, b string) bool {
	return containsPath(a, b) || containsPath(b, a)
}

func containsPath(parent, child string) bool {
	if runtime.GOOS == "windows" {
		parent = strings.ToLower(parent)
		child = strings.ToLower(child)
	}
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if relative == "." {
		return true
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validateResolvedSeparation(paths Paths) error {
	stateRoot, err := resolveExistingPrefix(paths.stateRoot)
	if err != nil {
		return err
	}
	root, err := resolveExistingPrefix(paths.Root)
	if err != nil {
		return err
	}
	runs, err := resolveExistingPrefix(paths.Runs)
	if err != nil {
		return err
	}
	if !samePath(stateRoot, paths.stateRoot) || !samePath(root, paths.Root) || !samePath(runs, paths.Runs) || !containsPath(stateRoot, root) || !containsPath(root, runs) {
		return errors.New("controller state path resolves through an alias or outside its configured namespace")
	}
	for _, protected := range paths.protected {
		resolved, resolveErr := resolveExistingPrefix(protected)
		if resolveErr != nil {
			return resolveErr
		}
		if pathsOverlap(stateRoot, resolved) || pathsOverlap(root, resolved) || pathsOverlap(runs, resolved) {
			return errors.New("controller state root resolves into repository or Git control paths")
		}
	}
	return nil
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func readMarker(path string) ([]byte, error) {
	directory := filepath.Dir(path)
	if err := safepath.Directory(directory); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var data bytes.Buffer
	_, size, _, exists, err := safepath.CopyRegular(root, markerName, maxMarkerBytes, &data)
	if err != nil {
		return nil, err
	}
	if !exists || size == 0 {
		return nil, os.ErrNotExist
	}
	return data.Bytes(), nil
}

func resolveExistingPrefix(path string) (string, error) {
	current := filepath.Clean(path)
	tail := []string{}
	for {
		if _, err := os.Lstat(current); err == nil {
			if err := safepath.Directory(current); err != nil {
				return "", err
			}
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(tail) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, tail[index])
			}
			return filepath.Clean(resolved), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("controller path has no existing filesystem prefix")
		}
		tail = append(tail, filepath.Base(current))
		current = parent
	}
}
