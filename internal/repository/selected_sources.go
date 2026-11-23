package repository

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/iancuuandrei/fabric/internal/gitexec"
	"github.com/iancuuandrei/fabric/internal/safepath"
)

// CopySelectedSourceBatch copies only the exact regular leaves supplied from a
// prior committed-tree observation. It revalidates each selected entry while
// traversing the same immutable tree and queries sizes before requesting blob
// contents through one cat-file batch-command process. Returning a nil writer
// with no error from open skips that object's contents and visits it with a nil
// digest, so callers can omit oversized blobs without draining them.
func CopySelectedSourceBatch(ctx context.Context, identity Identity, selected []SourceEntry, open func(SourceEntry, int64) (io.WriteCloser, error), visit func(SourceEntry, *SourceDigest) error) error {
	identityID, err := identity.ID()
	if err != nil {
		return err
	}
	if open == nil || visit == nil || len(selected) == 0 || len(selected) > 4096 {
		return errors.New("invalid selected source batch input")
	}
	byPath := make(map[string]SourceEntry, len(selected))
	for _, entry := range selected {
		if err := safepath.Relative(entry.Path); err != nil || entry.Kind != "file" || (entry.Mode != "100644" && entry.Mode != "100755") || len(entry.Object) != len(identity.Commit) || strings.Trim(entry.Object, "0123456789abcdef") != "" {
			return errors.New("selected source entry is invalid")
		}
		if _, exists := byPath[entry.Path]; exists {
			return errors.New("selected source batch repeats a path")
		}
		byPath[entry.Path] = entry
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	command := gitexec.CommandContext(ctx, "--no-optional-locks", "--no-replace-objects", "-C", identity.Root, "cat-file", "--batch-command")
	command.WaitDelay = time.Second
	for _, environment := range os.Environ() {
		key, _, _ := strings.Cut(environment, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			command.Env = append(command.Env, environment)
		}
	}
	command.Env = append(command.Env, "GIT_NO_LAZY_FETCH=1")
	input, err := command.StdinPipe()
	if err != nil {
		return err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return err
	}
	var diagnostic bounded
	command.Stderr = &diagnostic
	if err := command.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return err
	}
	waited := false
	defer func() {
		_ = input.Close()
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
		_ = output.Close()
	}()

	reader := bufio.NewReaderSize(output, 4096)
	buffer := make([]byte, 32<<10)
	sink := &treeSink{oidLength: len(identity.Commit), maxCount: 500000, timeout: 5 * time.Minute}
	sink.visit = func(observed SourceEntry) error {
		wanted, selectedPath := byPath[observed.Path]
		if !selectedPath {
			return nil
		}
		if wanted != observed {
			return fmt.Errorf("selected source entry changed in committed tree: %q", observed.Path)
		}
		delete(byPath, observed.Path)
		if _, err := fmt.Fprintln(input, "info "+observed.Object); err != nil {
			return errors.New("selected batch request failed")
		}
		header, err := reader.ReadSlice('\n')
		if err != nil {
			return errors.New("incomplete or oversized selected batch header")
		}
		fields := strings.Fields(string(header))
		if len(fields) != 3 || fields[0] != observed.Object || fields[1] != "blob" {
			return errors.New("selected batch blob identity mismatch")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return errors.New("selected batch blob size is invalid")
		}
		writer, err := open(observed, size)
		if err != nil {
			if writer != nil {
				_ = writer.Close()
			}
			return err
		}
		if writer == nil {
			return visit(observed, nil)
		}
		if size > 64<<20 {
			_ = writer.Close()
			return errors.New("selected batch blob exceeds repository read bound")
		}
		if _, err := fmt.Fprintln(input, "contents "+observed.Object); err != nil {
			_ = writer.Close()
			return errors.New("selected batch contents request failed")
		}
		header, err = reader.ReadSlice('\n')
		if err != nil {
			_ = writer.Close()
			return errors.New("incomplete or oversized selected contents header")
		}
		fields = strings.Fields(string(header))
		if len(fields) != 3 || fields[0] != observed.Object || fields[1] != "blob" {
			_ = writer.Close()
			return errors.New("selected contents blob identity mismatch")
		}
		contentSize, sizeErr := strconv.ParseInt(fields[2], 10, 64)
		if sizeErr != nil || contentSize != size {
			_ = writer.Close()
			return errors.New("selected contents size changed after preflight")
		}
		hash := sha256.New()
		copied, copyErr := io.CopyBuffer(io.MultiWriter(hash, writer), io.LimitReader(reader, contentSize), buffer)
		closeErr := writer.Close()
		if copyErr != nil || copied != contentSize || closeErr != nil {
			return errors.New("incomplete selected batch blob")
		}
		separator, err := reader.ReadByte()
		if err != nil || separator != '\n' {
			return errors.New("invalid selected batch blob delimiter")
		}
		digest := SourceDigest{RepositoryID: identityID, Commit: identity.Commit, Path: observed.Path, Blob: observed.Object, Bytes: contentSize, SHA256: hex.EncodeToString(hash.Sum(nil))}
		return visit(observed, &digest)
	}
	if err := streamTree(ctx, identity, sink); err != nil {
		return err
	}
	if len(byPath) != 0 {
		return errors.New("one or more selected paths are absent from committed tree")
	}
	if err := input.Close(); err != nil {
		return err
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		return errors.New("unexpected selected batch trailing output")
	}
	err = command.Wait()
	waited = true
	if err != nil || diagnostic.overflow {
		return errors.New("selected batch process failed")
	}
	return nil
}
