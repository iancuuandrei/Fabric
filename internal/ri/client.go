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

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/repository"
)

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
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
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
	if out.overflow || diagnostic.overflow {
		return nil, errors.New("RI process output exceeded bounds")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normal, err := canonical.Normalize(out.Bytes())
	if err != nil || !bytes.Equal(normal, out.Bytes()) {
		return nil, errors.New("RI returned noncanonical response")
	}
	var response struct {
		Version int             `json:"version"`
		OK      bool            `json:"ok"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *string         `json:"error,omitempty"`
	}
	if err := canonical.Decode(normal, &response); err != nil {
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
