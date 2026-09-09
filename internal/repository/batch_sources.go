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
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// VisitSourceDigests enumerates the exact committed tree and hashes regular
// blobs through one cat-file process. Unsupported leaves are explicitly visited
// with a nil digest. Any failure invalidates the complete observation. Limits:
// 500,000 leaves, 64MiB per blob, five minutes, one bounded streaming buffer.
func VisitSourceDigests(ctx context.Context, identity Identity, visit func(SourceEntry, *SourceDigest) error) error {
	return visitSourceBatch(ctx, identity, nil, visit)
}

// CopySourceBatch additionally copies each regular blob to a caller-created
// destination. Every successfully opened writer is closed before digest delivery,
// including on copy failure. The caller owns staging and synchronization; a failed
// run may leave partial files and grants no complete-copy evidence.
func CopySourceBatch(ctx context.Context, identity Identity, open func(SourceEntry, int64) (io.WriteCloser, error), visit func(SourceEntry, *SourceDigest) error) error {
	if open == nil {
		return errors.New("batch source destination factory required")
	}
	return visitSourceBatch(ctx, identity, open, visit)
}

func visitSourceBatch(ctx context.Context, identity Identity, open func(SourceEntry, int64) (io.WriteCloser, error), visit func(SourceEntry, *SourceDigest) error) error {
	id, err := identity.ID()
	if err != nil {
		return err
	}
	if visit == nil {
		return errors.New("source digest visitor required")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "--no-optional-locks", "--no-replace-objects", "-C", identity.Root, "cat-file", "--batch")
	cmd.WaitDelay = time.Second
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return err
	}
	var diagnostic bounded
	cmd.Stderr = &diagnostic
	if err := cmd.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return err
	}
	waited := false
	defer func() {
		_ = input.Close()
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		_ = output.Close()
	}()
	reader := bufio.NewReaderSize(output, 4096)
	buffer := make([]byte, 32<<10)
	sink := &treeSink{oidLength: len(identity.Commit), maxCount: 500000, timeout: 5 * time.Minute}
	sink.visit = func(entry SourceEntry) error {
		if entry.Kind != "file" {
			return visit(entry, nil)
		}
		if _, err := fmt.Fprintln(input, entry.Object); err != nil {
			return errors.New("batch request failed")
		}
		header, err := reader.ReadSlice('\n')
		if err != nil {
			return errors.New("incomplete or oversized batch header")
		}
		fields := strings.Fields(string(header))
		if len(fields) != 3 || fields[0] != entry.Object || fields[1] != "blob" {
			return errors.New("batch blob identity mismatch")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 || size > 64<<20 {
			return errors.New("unsupported batch blob size")
		}
		hash := sha256.New()
		var destination io.Writer = hash
		var closer io.WriteCloser
		if open != nil {
			closer, err = open(entry, size)
			if err != nil {
				if closer != nil {
					_ = closer.Close()
				}
				return err
			}
			if closer == nil {
				return errors.New("batch destination missing")
			}
			destination = io.MultiWriter(hash, closer)
		}
		copied, err := io.CopyBuffer(destination, io.LimitReader(reader, size), buffer)
		var closeErr error
		if closer != nil {
			closeErr = closer.Close()
		}
		if err != nil || copied != size || closeErr != nil {
			return errors.New("incomplete batch blob")
		}
		separator, err := reader.ReadByte()
		if err != nil || separator != '\n' {
			return errors.New("invalid batch blob delimiter")
		}
		digest := SourceDigest{RepositoryID: id, Commit: identity.Commit, Path: entry.Path, Blob: entry.Object, Bytes: size, SHA256: hex.EncodeToString(hash.Sum(nil))}
		return visit(entry, &digest)
	}
	if err := streamTree(ctx, identity, sink); err != nil {
		return err
	}
	if err := input.Close(); err != nil {
		return err
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		return errors.New("unexpected batch trailing output")
	}
	err = cmd.Wait()
	waited = true
	if err != nil || diagnostic.overflow {
		return errors.New("batch process failed")
	}
	return nil
}
