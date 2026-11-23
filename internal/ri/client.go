package ri

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
)

// ErrProcessUnavailable marks a failure to obtain a usable response because
// the pinned RI child process could not complete normally. It does not cover
// executable pin failures, protocol errors, or RI-declared request rejection.
var ErrProcessUnavailable = errors.New("RI process unavailable")

// processUnavailableError is an optional-capability failure from the RI
// subprocess boundary. Process details are not exposed, and the error is not
// unwrapped: callers may degrade only on this exact terminal error, not on
// arbitrary errors joined to an underlying process failure.
type processUnavailableError struct{}

// Error returns a stable diagnostic without exposing process stderr.
func (e *processUnavailableError) Error() string { return ErrProcessUnavailable.Error() }

// Is identifies the one typed optional-capability condition.
func (e *processUnavailableError) Is(target error) bool { return target == ErrProcessUnavailable }

func unavailableProcess() error { return &processUnavailableError{} }

// IsProcessUnavailableOnly reports whether every leaf in err's unwrap tree is
// the typed RI process-unavailable condition. It rejects joins that also
// contain a strict failure, such as a lease-close or candidate-integrity error.
func IsProcessUnavailableOnly(err error) bool {
	if err == nil {
		return false
	}
	if err == ErrProcessUnavailable {
		return true
	}
	if _, ok := err.(*processUnavailableError); ok {
		return true
	}
	if many, ok := err.(interface{ Unwrap() []error }); ok {
		children := many.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !IsProcessUnavailableOnly(child) {
				return false
			}
		}
		return true
	}
	if one, ok := err.(interface{ Unwrap() error }); ok {
		return IsProcessUnavailableOnly(one.Unwrap())
	}
	return false
}

// Source is the immutable source binding understood by Rust RI.
type Source struct {
	RepositoryID string `json:"repository_id"`
	ObjectFormat string `json:"object_format"`
	Commit       string `json:"commit"`
	Tree         string `json:"tree"`
}

// FromRepository derives the RI source binding from validated controller identity.
func FromRepository(identity repository.Identity) (Source, error) {
	id, err := identity.ID()
	if err != nil {
		return Source{}, err
	}
	return Source{id, identity.ObjectFormat, identity.Commit, identity.Tree}, nil
}

// Client identifies one pinned Rust executable. It never installs or builds it.
type Client struct {
	Executable     string
	ExecutableHash string
}

// Call executes one canonical request with a 30-second bound and no automatic retry.
// It admits only canonical v1 successful results; errors grant no snapshot authority.
func (c Client) Call(ctx context.Context, request any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.validateExecutable(); err != nil {
		return nil, err
	}
	input, err := canonical.Bytes(request)
	if err != nil {
		return nil, err
	}
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(parentCtx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, c.Executable, "--stdio")
	command.WaitDelay = time.Second
	command.Env = []string{}
	for _, key := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP"} {
		if value, ok := os.LookupEnv(key); ok {
			command.Env = append(command.Env, key+"="+value)
		}
	}
	command.Stdin = bytes.NewReader(input)
	out := limited{limit: canonical.MaxBytes, cancel: cancel}
	diagnostic := limited{limit: 64 << 10, cancel: cancel}
	command.Stdout = &out
	command.Stderr = &diagnostic
	runErr := command.Run()
	if err := parentCtx.Err(); err != nil {
		return nil, err
	}
	if out.overflow || diagnostic.overflow {
		return nil, unavailableProcess()
	}
	if err := ctx.Err(); err != nil {
		return nil, unavailableProcess()
	}
	normal, err := canonical.Normalize(out.Bytes())
	if err != nil || !bytes.Equal(normal, out.Bytes()) {
		if runErr != nil {
			return nil, unavailableProcess()
		}
		return nil, errors.New("RI returned noncanonical response")
	}
	var response struct {
		Version int             `json:"version"`
		OK      bool            `json:"ok"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *string         `json:"error,omitempty"`
	}
	if err := canonical.Decode(normal, &response); err != nil {
		if runErr != nil {
			return nil, unavailableProcess()
		}
		return nil, err
	}
	if response.Version != 1 {
		return nil, errors.New("unsupported RI response version")
	}
	if !response.OK {
		if response.Error == nil || *response.Error == "" || len(response.Result) != 0 {
			return nil, errors.New("invalid RI failure response")
		}
		return nil, errors.New("RI rejected request: " + *response.Error)
	}
	if runErr != nil || response.Error != nil || len(response.Result) == 0 || response.Result[0] != '{' {
		if runErr != nil {
			return nil, unavailableProcess()
		}
		return nil, errors.New("RI success response lacks successful process or result")
	}
	return response.Result, nil
}

type limited struct {
	bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

// Write retains a bounded prefix and cancels the process on overflow.
func (w *limited) Write(data []byte) (int, error) {
	n := len(data)
	remaining := w.limit - w.Len()
	if len(data) > remaining {
		w.overflow = true
		data = data[:remaining]
		w.cancel()
	}
	_, err := w.Buffer.Write(data)
	return n, err
}

// ValidateExecutable checks the immutable parser pin without starting a process.
func (c Client) ValidateExecutable() error { return c.validateExecutable() }

func (c Client) validateExecutable() error {
	if !filepath.IsAbs(c.Executable) || len(c.ExecutableHash) != 64 || strings.Trim(c.ExecutableHash, "0123456789abcdef") != "" {
		return errors.New("absolute pinned RI executable required")
	}
	file, err := os.Open(c.Executable)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 256<<20 {
		file.Close()
		return errors.New("invalid RI executable type or size")
	}
	hash := sha256.New()
	n, readErr := io.Copy(hash, io.LimitReader(file, (256<<20)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || n > 256<<20 || hex.EncodeToString(hash.Sum(nil)) != c.ExecutableHash {
		return errors.New("RI executable hash mismatch or unreadable executable")
	}
	return nil
}
