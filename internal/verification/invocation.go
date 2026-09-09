package verification

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/safepath"
)

const environmentPolicy = "harness.verification-environment.v1"

var environmentNames = map[string]bool{"PATH": true, "HOME": true, "USERPROFILE": true, "SYSTEMROOT": true, "WINDIR": true, "TEMP": true, "TMP": true, "LANG": true, "LC_ALL": true, "NUMBER_OF_PROCESSORS": true, "GOCACHE": true, "GOMODCACHE": true, "GOPATH": true, "GOROOT": true, "GOTOOLCHAIN": true, "RUSTUP_HOME": true, "CARGO_HOME": true, "DOTNET_ROOT": true, "JAVA_HOME": true}

// Executable identifies the resolved root binary, not its dynamic dependencies.
type Executable struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

// Invocation freezes required-check inputs and the exact selected environment.
// A nil executable and nonempty Unavailable records a dependency failure explicitly.
type Invocation struct {
	Version           int               `json:"version"`
	CandidateID       string            `json:"candidate_id"`
	Check             config.Check      `json:"check"`
	Directory         string            `json:"directory"`
	EnvironmentPolicy string            `json:"environment_policy"`
	Environment       map[string]string `json:"environment"`
	EnvironmentHash   string            `json:"environment_hash"`
	Executable        *Executable       `json:"executable"`
	Unavailable       string            `json:"unavailable"`
}

func binaryHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 256<<20 {
		return "", errors.New("executable type/size unsupported")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (256<<20)+1))
	if err != nil {
		return "", err
	}
	if n > 256<<20 {
		return "", errors.New("executable exceeded hash bound")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func environment() map[string]string {
	result := map[string]string{}
	for _, entry := range os.Environ() {
		k, v, ok := strings.Cut(entry, "=")
		if ok && environmentNames[strings.ToUpper(k)] {
			result[k] = v
		}
	}
	return result
}

func environmentID(values map[string]string) (string, error) {
	return canonical.Hash(environmentPolicy, values)
}

// Prepare freezes a check against a candidate. Missing tools remain a valid
// NOT_RUN invocation; malformed policy is an error before dispatch.
func Prepare(candidateID, directory string, check config.Check) (Invocation, error) {
	if len(check.Argv) == 0 {
		return Invocation{}, errors.New("empty verification argv")
	}
	i := Invocation{Version: 1, CandidateID: candidateID, Check: check, Directory: directory, EnvironmentPolicy: environmentPolicy, Environment: environment()}
	var err error
	i.EnvironmentHash, err = environmentID(i.Environment)
	if err != nil {
		return i, err
	}
	path, err := exec.LookPath(check.Argv[0])
	if err == nil {
		path, err = filepath.Abs(path)
	}
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	if err == nil {
		var hash string
		hash, err = binaryHash(path)
		if err == nil {
			i.Executable = &Executable{path, hash}
		}
	}
	if err != nil {
		i.Unavailable = "required executable could not be resolved or hashed"
	}
	return i, i.Validate()
}

// Validate enforces invocation identity and the finite environment policy.
func (i Invocation) Validate() error {
	if i.Version != 1 || !filepath.IsAbs(i.Directory) || i.EnvironmentPolicy != environmentPolicy || i.Environment == nil {
		return errors.New("invalid verification invocation")
	}
	if err := safepath.RequireDigest(i.CandidateID); err != nil {
		return err
	}
	if strings.TrimSpace(i.Check.Name) == "" || len(i.Check.Argv) == 0 || len(i.Check.Argv) > 128 || strings.TrimSpace(i.Check.Argv[0]) == "" || i.Check.TimeoutSeconds < 1 || i.Check.TimeoutSeconds > 86400 {
		return errors.New("invalid verification check")
	}
	for _, arg := range i.Check.Argv {
		if strings.ContainsRune(arg, 0) || len(arg) > 8192 {
			return errors.New("invalid verification argv")
		}
	}
	seen := map[string]bool{}
	for k, v := range i.Environment {
		upper := strings.ToUpper(k)
		if !environmentNames[upper] || seen[upper] || strings.ContainsRune(v, 0) {
			return errors.New("undeclared or duplicate environment entry")
		}
		seen[upper] = true
	}
	hash, err := environmentID(i.Environment)
	if err != nil {
		return err
	}
	if hash != i.EnvironmentHash {
		return errors.New("environment identity mismatch")
	}
	if i.Executable == nil {
		if i.Unavailable == "" {
			return errors.New("unavailable executable needs explanation")
		}
	} else {
		if i.Unavailable != "" || !filepath.IsAbs(i.Executable.Path) {
			return errors.New("invalid executable identity")
		}
		if err := safepath.RequireDigest(i.Executable.Hash); err != nil {
			return err
		}
	}
	return nil
}

// ID hashes every admitted input, including explicit unknown-tool evidence.
func (i Invocation) ID() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash("harness.verification-invocation.v1", i)
}

func (i Invocation) env() []string {
	keys := make([]string, 0, len(i.Environment))
	for k := range i.Environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, k := range keys {
		result = append(result, k+"="+i.Environment[k])
	}
	return result
}
