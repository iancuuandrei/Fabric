package verification

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

const (
	goFormatObservationVersion       = 1
	goFormatManifestVersion          = 1
	goFormatEngineVersion            = "harness.go-format-source.v1"
	goFormatEnvironmentPolicy        = "harness.in-process-go-format.v1"
	goFormatVirtualCWD               = "/"
	goFormatMaxFiles                 = 24
	goFormatMaxFileBytes             = 32 << 10
	goFormatMaxInputBytes            = goFormatMaxFiles * goFormatMaxFileBytes
	goFormatMaxMetadataBytes         = 16 << 10
	goFormatMaxPayloadBytes          = goFormatMaxInputBytes
	goFormatCacheMaxEntries          = 64
	goFormatCacheMaxBytes      int64 = 48 << 20
)

// GoFormatManifest declares every candidate file supplied to the fixed
// in-process formatter. It has no glob, directory walk, argv, environment, or
// current-working-directory input.
type GoFormatManifest struct {
	Version int      `json:"version"`
	Paths   []string `json:"paths"`
}

// GoFormatEngine binds the compiled formatter implementation and host runtime.
type GoFormatEngine struct {
	Version           string `json:"version"`
	Executable        string `json:"executable"`
	ExecutableSHA256  string `json:"executable_sha256"`
	Runtime           string `json:"runtime"`
	GOOS              string `json:"goos"`
	GOARCH            string `json:"goarch"`
	EnvironmentPolicy string `json:"environment_policy"`
	VirtualCWD        string `json:"virtual_cwd"`
}

// GoFormatInput is one exact candidate source byte binding.
type GoFormatInput struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// GoFormatAction is the complete, fixed formatter input closure.
type GoFormatAction struct {
	Version     int                `json:"version"`
	Candidate   worktree.Candidate `json:"candidate"`
	CandidateID string             `json:"candidate_id"`
	WorkspaceID string             `json:"workspace_id"`
	Engine      GoFormatEngine     `json:"engine"`
	Inputs      []GoFormatInput    `json:"inputs"`
}

// GoFormatFileObservation is a non-authoritative formatting artifact. Outcome
// is unchanged, formatted, or format_error; it is never PASS or FAIL.
type GoFormatFileObservation struct {
	Path             string `json:"path"`
	InputSHA256      string `json:"input_sha256"`
	InputBytes       int64  `json:"input_bytes"`
	Outcome          string `json:"outcome"`
	FormattedSHA256  string `json:"formatted_sha256"`
	FormattedBytes   int64  `json:"formatted_bytes"`
	PayloadOffset    int64  `json:"payload_offset"`
	DiagnosticSHA256 string `json:"diagnostic_sha256"`
}

// GoFormatObservation records a bounded formatting artifact. It carries no
// verification, review, repair, or readiness authority.
type GoFormatObservation struct {
	Version       int                       `json:"version"`
	Action        GoFormatAction            `json:"action"`
	ActionID      string                    `json:"action_id"`
	Files         []GoFormatFileObservation `json:"files"`
	PayloadSHA256 string                    `json:"payload_sha256"`
	PayloadBytes  int64                     `json:"payload_bytes"`
	ArtifactID    string                    `json:"artifact_id"`
}

// GoFormatCacheState is local diagnostic data. It is not part of the action or
// observation identity and is never persisted in a controller journal.
type GoFormatCacheState string

const (
	// GoFormatCacheHit reports a validated artifact reused for the exact action.
	GoFormatCacheHit GoFormatCacheState = "hit"
	// GoFormatCacheMiss reports a newly computed and published artifact.
	GoFormatCacheMiss GoFormatCacheState = "miss"
	// GoFormatCacheContended reports an observation without artifact publication.
	GoFormatCacheContended GoFormatCacheState = "contended"
)

// Validate rejects a manifest that could let unlisted or sensitive inputs enter
// the fixed formatter action.
func (m GoFormatManifest) Validate() error {
	if m.Version != goFormatManifestVersion || len(m.Paths) < 1 || len(m.Paths) > goFormatMaxFiles {
		return errors.New("invalid Go format manifest shape")
	}
	for index, path := range m.Paths {
		if safepath.Relative(path) != nil || !taskcontext.EligiblePath(path) || filepath.Ext(path) != ".go" || index > 0 && m.Paths[index-1] >= path {
			return errors.New("invalid Go format manifest path")
		}
	}
	return nil
}

// Validate requires the exact executable, runtime and fixed environment binding.
func (e GoFormatEngine) Validate() error {
	if e.Version != goFormatEngineVersion || !filepath.IsAbs(e.Executable) || safepath.RequireDigest(e.ExecutableSHA256) != nil || e.Runtime == "" || e.GOOS == "" || e.GOARCH == "" || e.EnvironmentPolicy != goFormatEnvironmentPolicy || e.VirtualCWD != goFormatVirtualCWD {
		return errors.New("invalid Go format engine binding")
	}
	return nil
}

// ID returns the exact formatter action identity. Callers must validate the
// candidate against the actual workspace binding before constructing it.
func (a GoFormatAction) ID() (string, error) {
	if a.Version != goFormatObservationVersion || a.CandidateID == "" || safepath.RequireDigest(a.CandidateID) != nil || safepath.RequireDigest(a.WorkspaceID) != nil {
		return "", errors.New("invalid Go format action")
	}
	if err := a.Engine.Validate(); err != nil {
		return "", err
	}
	if len(a.Inputs) < 1 || len(a.Inputs) > goFormatMaxFiles {
		return "", errors.New("invalid Go format action inputs")
	}
	var total int64
	for index, input := range a.Inputs {
		if safepath.Relative(input.Path) != nil || !taskcontext.EligiblePath(input.Path) || filepath.Ext(input.Path) != ".go" || safepath.RequireDigest(input.SHA256) != nil || input.Bytes < 0 || input.Bytes > goFormatMaxFileBytes || index > 0 && a.Inputs[index-1].Path >= input.Path {
			return "", errors.New("invalid Go format action input")
		}
		total += input.Bytes
	}
	if total > goFormatMaxInputBytes {
		return "", errors.New("Go format action input bound exceeded")
	}
	candidateID, err := a.Candidate.ID()
	if err != nil || candidateID != a.CandidateID || a.WorkspaceID != a.Candidate.WorktreeID {
		return "", errors.New("Go format candidate binding mismatch")
	}
	return canonical.Hash("harness.go-format-action.v1", a)
}

// ObserveGoFormat obtains a bounded exact-source artifact for the supplied
// candidate. It is deliberately separate from generic verification and never
// mutates a journal or workspace.
func ObserveGoFormat(ctx context.Context, binding worktree.Binding, expected worktree.Candidate, manifest GoFormatManifest, cacheDir string) (GoFormatObservation, GoFormatCacheState, error) {
	if err := manifest.Validate(); err != nil {
		return GoFormatObservation{}, "", err
	}
	if err := expected.ValidateBinding(binding); err != nil {
		return GoFormatObservation{}, "", err
	}
	if err := validateFormatCacheDir(cacheDir, binding); err != nil {
		return GoFormatObservation{}, "", err
	}
	beforeEngine, err := currentGoFormatEngine()
	if err != nil {
		return GoFormatObservation{}, "", err
	}
	lease, err := worktree.AcquireRead(binding.Request)
	if err != nil {
		return GoFormatObservation{}, "", err
	}
	var observation GoFormatObservation
	var state GoFormatCacheState
	observeErr := lease.WithOwnership(binding.Request, func(worktree.LeaseIdentity) error {
		files, err := worktree.ReadSources(ctx, binding, expected, manifest.Paths, goFormatMaxFileBytes)
		if err != nil {
			return err
		}
		action, contents, err := formatAction(binding, expected, beforeEngine, files)
		if err != nil {
			return err
		}
		actionID, err := action.ID()
		if err != nil {
			return err
		}
		if cached, ok := readGoFormatCache(cacheDir, action, actionID); ok {
			observation, state = cached, GoFormatCacheHit
			return stableFormatEngineAndCandidate(ctx, binding, expected, beforeEngine)
		}
		computed, payload, err := computeGoFormatObservation(action, actionID, contents)
		if err != nil {
			return err
		}
		state = GoFormatCacheMiss
		if acquired := tryGoFormatCacheLock(cacheDir); acquired != nil {
			defer acquired()
			if cached, ok := readGoFormatCache(cacheDir, action, actionID); ok {
				observation, state = cached, GoFormatCacheHit
			} else if err := removeGoFormatCacheEntry(cacheDir, actionID); err != nil {
				return err
			} else if err := writeGoFormatCache(cacheDir, computed, payload); err == nil {
				observation = computed
			} else {
				return err
			}
		} else {
			observation, state = computed, GoFormatCacheContended
		}
		return stableFormatEngineAndCandidate(ctx, binding, expected, beforeEngine)
	})
	closeErr := lease.Close()
	if closeErr != nil {
		return GoFormatObservation{}, "", errors.Join(closeErr, observeErr)
	}
	if observeErr != nil {
		return GoFormatObservation{}, "", observeErr
	}
	return observation, state, nil
}

func formatAction(binding worktree.Binding, expected worktree.Candidate, engine GoFormatEngine, files []worktree.SourceFile) (GoFormatAction, [][]byte, error) {
	if len(files) == 0 || len(files) > goFormatMaxFiles {
		return GoFormatAction{}, nil, errors.New("invalid Go format source count")
	}
	candidateID, err := expected.ID()
	if err != nil {
		return GoFormatAction{}, nil, err
	}
	workspaceID, err := binding.ID()
	if err != nil {
		return GoFormatAction{}, nil, err
	}
	action := GoFormatAction{Version: goFormatObservationVersion, Candidate: expected, CandidateID: candidateID, WorkspaceID: workspaceID, Engine: engine, Inputs: make([]GoFormatInput, len(files))}
	contents := make([][]byte, len(files))
	var total int64
	for index, file := range files {
		if file.Err != nil || file.CandidateID != candidateID || safepath.Relative(file.Path) != nil || !taskcontext.EligiblePath(file.Path) || filepath.Ext(file.Path) != ".go" || safepath.RequireDigest(file.SHA256) != nil || file.Size < 0 || file.Size > goFormatMaxFileBytes || file.NextOffset != nil || int64(len(file.Content)) != file.Size {
			return GoFormatAction{}, nil, errors.New("Go format source binding incomplete")
		}
		if index > 0 && files[index-1].Path >= file.Path {
			return GoFormatAction{}, nil, errors.New("Go format source ordering invalid")
		}
		total += file.Size
		action.Inputs[index] = GoFormatInput{Path: file.Path, SHA256: file.SHA256, Bytes: file.Size}
		contents[index] = append([]byte(nil), file.Content...)
	}
	if total > goFormatMaxInputBytes {
		return GoFormatAction{}, nil, errors.New("Go format source aggregate bound exceeded")
	}
	return action, contents, nil
}

func computeGoFormatObservation(action GoFormatAction, actionID string, contents [][]byte) (GoFormatObservation, []byte, error) {
	if len(contents) != len(action.Inputs) {
		return GoFormatObservation{}, nil, errors.New("Go format content/action mismatch")
	}
	observation := GoFormatObservation{Version: goFormatObservationVersion, Action: action, ActionID: actionID, Files: make([]GoFormatFileObservation, len(contents))}
	payload := bytes.Buffer{}
	for index, content := range contents {
		input := action.Inputs[index]
		formatted, err := format.Source(content)
		file := GoFormatFileObservation{Path: input.Path, InputSHA256: input.SHA256, InputBytes: input.Bytes, PayloadOffset: int64(payload.Len())}
		if err != nil {
			diagnostic := sha256.Sum256([]byte(err.Error()))
			file.Outcome = "format_error"
			file.DiagnosticSHA256 = hex.EncodeToString(diagnostic[:])
		} else {
			if len(formatted) > goFormatMaxFileBytes || payload.Len()+len(formatted) > goFormatMaxPayloadBytes {
				return GoFormatObservation{}, nil, errors.New("Go format output bound exceeded")
			}
			sum := sha256.Sum256(formatted)
			file.FormattedSHA256 = hex.EncodeToString(sum[:])
			file.FormattedBytes = int64(len(formatted))
			file.Outcome = "formatted"
			if bytes.Equal(content, formatted) {
				file.Outcome = "unchanged"
			}
			_, _ = payload.Write(formatted)
		}
		observation.Files[index] = file
	}
	payloadSum := sha256.Sum256(payload.Bytes())
	observation.PayloadSHA256 = hex.EncodeToString(payloadSum[:])
	observation.PayloadBytes = int64(payload.Len())
	artifactID, err := canonical.Hash("harness.go-format-artifact.v1", observation)
	if err != nil {
		return GoFormatObservation{}, nil, err
	}
	observation.ArtifactID = artifactID
	if err := validateGoFormatObservation(observation, payload.Bytes()); err != nil {
		return GoFormatObservation{}, nil, err
	}
	return observation, payload.Bytes(), nil
}

func validateGoFormatObservation(observation GoFormatObservation, payload []byte) error {
	actionID, err := observation.Action.ID()
	if err != nil || actionID != observation.ActionID || observation.Version != goFormatObservationVersion || len(observation.Files) != len(observation.Action.Inputs) || observation.PayloadBytes != int64(len(payload)) || len(payload) > goFormatMaxPayloadBytes || safepath.RequireDigest(observation.PayloadSHA256) != nil || safepath.RequireDigest(observation.ArtifactID) != nil {
		return errors.New("invalid Go format observation")
	}
	payloadSum := sha256.Sum256(payload)
	if observation.PayloadSHA256 != hex.EncodeToString(payloadSum[:]) {
		return errors.New("Go format payload identity mismatch")
	}
	var position int64
	for index, file := range observation.Files {
		input := observation.Action.Inputs[index]
		if file.Path != input.Path || file.InputSHA256 != input.SHA256 || file.InputBytes != input.Bytes || file.PayloadOffset != position {
			return errors.New("Go format file/action mismatch")
		}
		switch file.Outcome {
		case "formatted", "unchanged":
			if safepath.RequireDigest(file.FormattedSHA256) != nil || file.FormattedBytes < 0 || file.FormattedBytes > goFormatMaxFileBytes || file.DiagnosticSHA256 != "" || position+file.FormattedBytes > int64(len(payload)) {
				return errors.New("invalid Go format output observation")
			}
			segment := payload[position : position+file.FormattedBytes]
			sum := sha256.Sum256(segment)
			if file.FormattedSHA256 != hex.EncodeToString(sum[:]) {
				return errors.New("Go format output digest mismatch")
			}
			if file.Outcome == "unchanged" && file.FormattedSHA256 != input.SHA256 || file.Outcome == "formatted" && file.FormattedSHA256 == input.SHA256 {
				return errors.New("Go format outcome does not match source identity")
			}
			position += file.FormattedBytes
		case "format_error":
			if file.FormattedSHA256 != "" || file.FormattedBytes != 0 || safepath.RequireDigest(file.DiagnosticSHA256) != nil {
				return errors.New("invalid Go format error observation")
			}
		default:
			return errors.New("unknown Go format observation outcome")
		}
	}
	if position != int64(len(payload)) {
		return errors.New("Go format payload range mismatch")
	}
	withoutID := observation
	withoutID.ArtifactID = ""
	artifactID, err := canonical.Hash("harness.go-format-artifact.v1", withoutID)
	if err != nil || artifactID != observation.ArtifactID {
		return errors.New("Go format artifact identity mismatch")
	}
	return nil
}

func currentGoFormatEngine() (GoFormatEngine, error) {
	path, err := os.Executable()
	if err != nil {
		return GoFormatEngine{}, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return GoFormatEngine{}, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return GoFormatEngine{}, err
	}
	hash, err := formatExecutableHash(path)
	if err != nil {
		return GoFormatEngine{}, err
	}
	return GoFormatEngine{Version: goFormatEngineVersion, Executable: path, ExecutableSHA256: hash, Runtime: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, EnvironmentPolicy: goFormatEnvironmentPolicy, VirtualCWD: goFormatVirtualCWD}, nil
}

func stableFormatEngineAndCandidate(ctx context.Context, binding worktree.Binding, expected worktree.Candidate, before GoFormatEngine) error {
	after, err := currentGoFormatEngine()
	if err != nil || after != before {
		return errors.New("Go format engine changed during observation")
	}
	candidate, _, err := worktree.Capture(ctx, binding)
	if err != nil || candidate != expected {
		return errors.New("candidate changed during Go format observation")
	}
	return nil
}

func formatExecutableHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256<<20 {
		return "", errors.New("Go format executable type/size unsupported")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(file, (256<<20)+1))
	if err != nil || n > 256<<20 {
		return "", errors.New("Go format executable hash failed")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func validateFormatCacheDir(cacheDir string, binding worktree.Binding) error {
	volumeRoot := filepath.VolumeName(cacheDir) + string(filepath.Separator)
	if cacheDir == "" || !filepath.IsAbs(cacheDir) || filepath.Clean(cacheDir) != cacheDir || cacheDir == volumeRoot {
		return errors.New("Go format cache directory must be an absolute clean non-root path")
	}
	for _, protected := range []string{binding.Request.Path, binding.Request.Source.Root, binding.Request.Source.CommonDir} {
		if formatPathContains(protected, cacheDir) || formatPathContains(cacheDir, protected) {
			return errors.New("Go format cache must remain outside workspace and repository")
		}
	}
	ancestor := cacheDir
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return errors.New("Go format cache ancestor unavailable")
		}
		ancestor = parent
	}
	if err := safepath.Directory(ancestor); err != nil {
		return err
	}
	if filepath.Clean(ancestor) == filepath.Clean(cacheDir) {
		return nil
	}
	relative, err := filepath.Rel(ancestor, cacheDir)
	if err != nil || safepath.Relative(filepath.ToSlash(relative)) != nil {
		return errors.New("invalid Go format cache path")
	}
	if err := safepath.EnsureDirectory(ancestor, filepath.ToSlash(relative)); err != nil {
		return err
	}
	return safepath.Directory(cacheDir)
}

func formatPathContains(parent, child string) bool {
	relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		relative = strings.ToLower(relative)
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func tryGoFormatCacheLock(cacheDir string) func() {
	path := filepath.Join(cacheDir, ".go-format-observation.lock")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil
	}
	return func() {
		_ = file.Close()
		_ = os.Remove(path)
	}
}

func readGoFormatCache(cacheDir string, action GoFormatAction, actionID string) (GoFormatObservation, bool) {
	root, err := os.OpenRoot(cacheDir)
	if err != nil {
		return GoFormatObservation{}, false
	}
	defer root.Close()
	metadata, err := readFormatCacheFile(root, actionID+".json", goFormatMaxMetadataBytes)
	if err != nil {
		return GoFormatObservation{}, false
	}
	var observation GoFormatObservation
	if canonical.Decode(metadata, &observation) != nil || !reflect.DeepEqual(observation.Action, action) || observation.ActionID != actionID {
		return GoFormatObservation{}, false
	}
	payload, err := readFormatCacheFile(root, actionID+".out", goFormatMaxPayloadBytes)
	if err != nil {
		return GoFormatObservation{}, false
	}
	if validateGoFormatObservation(observation, payload) != nil {
		return GoFormatObservation{}, false
	}
	return observation, true
}

func readFormatCacheFile(root *os.Root, name string, limit int64) ([]byte, error) {
	var destination bytes.Buffer
	_, _, _, exists, err := safepath.CopyRegular(root, name, limit, &destination)
	if err != nil || !exists {
		return nil, errors.New("cache artifact absent or invalid")
	}
	return destination.Bytes(), nil
}

func removeGoFormatCacheEntry(cacheDir, key string) error {
	if safepath.RequireDigest(key) != nil {
		return errors.New("invalid Go format cache key")
	}
	root, err := os.OpenRoot(cacheDir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{key + ".json", key + ".out", key + ".json.tmp", key + ".out.tmp"} {
		if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func writeGoFormatCache(cacheDir string, observation GoFormatObservation, payload []byte) error {
	metadata, err := canonical.Bytes(observation)
	if err != nil || len(metadata) > goFormatMaxMetadataBytes || len(payload) > goFormatMaxPayloadBytes {
		return errors.New("Go format cache artifact exceeds bounds")
	}
	if err := ensureGoFormatCacheCapacity(cacheDir, int64(len(metadata)+len(payload))); err != nil {
		return err
	}
	root, err := os.OpenRoot(cacheDir)
	if err != nil {
		return err
	}
	defer root.Close()
	key := observation.ActionID
	if err := writeFormatCacheTemp(root, key+".out", payload); err != nil {
		return err
	}
	if err := writeFormatCacheTemp(root, key+".json", metadata); err != nil {
		_ = root.Remove(key + ".out")
		return err
	}
	return nil
}

func writeFormatCacheTemp(root *os.Root, name string, content []byte) error {
	if safepath.RequireDigest(strings.TrimSuffix(name, filepath.Ext(name))) != nil {
		return errors.New("invalid Go format cache entry")
	}
	temp := name + ".tmp"
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(content)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = root.Remove(temp)
		return err
	}
	if err := root.Rename(temp, name); err != nil {
		_ = root.Remove(temp)
		return err
	}
	return nil
}

type goFormatCacheEntry struct {
	key      string
	size     int64
	modified time.Time
}

type goFormatCacheRemover interface {
	Remove(name string) error
}

func trimGoFormatCache(remover goFormatCacheRemover, items []*goFormatCacheEntry, total, reserve int64) (int64, error) {
	for len(items) > 0 && (len(items) >= goFormatCacheMaxEntries || total+reserve > goFormatCacheMaxBytes) {
		item := items[0]
		items = items[1:]
		for _, suffix := range []string{".json", ".out"} {
			if err := remover.Remove(item.key + suffix); err != nil {
				return total, err
			}
		}
		total -= item.size
	}
	if total+reserve > goFormatCacheMaxBytes || len(items) >= goFormatCacheMaxEntries {
		return total, errors.New("Go format cache capacity unavailable")
	}
	return total, nil
}

func ensureGoFormatCacheCapacity(cacheDir string, reserve int64) error {
	if reserve < 0 || reserve > goFormatCacheMaxBytes {
		return errors.New("Go format cache reservation exceeds bound")
	}
	root, err := os.OpenRoot(cacheDir)
	if err != nil {
		return err
	}
	defer root.Close()
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return err
	}
	byKey := map[string]*goFormatCacheEntry{}
	for _, entry := range entries {
		name := entry.Name()
		if len(name) < 68 || name[64] != '.' || (name[65:] != "json" && name[65:] != "out") || safepath.RequireDigest(name[:64]) != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		current := byKey[name[:64]]
		if current == nil {
			current = &goFormatCacheEntry{key: name[:64], modified: info.ModTime()}
			byKey[current.key] = current
		}
		current.size += info.Size()
		if info.ModTime().Before(current.modified) {
			current.modified = info.ModTime()
		}
	}
	items := make([]*goFormatCacheEntry, 0, len(byKey))
	var total int64
	for _, item := range byKey {
		items = append(items, item)
		total += item.size
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].modified.Equal(items[j].modified) {
			return items[i].modified.Before(items[j].modified)
		}
		return items[i].key < items[j].key
	})
	_, err = trimGoFormatCache(root, items, total, reserve)
	return err
}
