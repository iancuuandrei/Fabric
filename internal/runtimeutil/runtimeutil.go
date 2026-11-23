package runtimeutil

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
)

const maxExecutableHashBytes = 512 << 20

// HashFile returns the lowercase SHA-256 hex digest of a regular file up to 512 MiB.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxExecutableHashBytes+1))
	if err != nil || n > maxExecutableHashBytes {
		return "", errors.New("executable could not be hashed within size bound")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyExecutableHash validates expectedHash and confirms executable matches it when non-empty.
func VerifyExecutableHash(executable, expectedHash, label string) error {
	if expectedHash == "" {
		return nil
	}
	if err := safepath.RequireDigest(expectedHash); err != nil {
		return err
	}
	actual, err := HashFile(executable)
	if err != nil {
		return fmt.Errorf("hash %s executable: %w", label, err)
	}
	if actual != expectedHash {
		return fmt.Errorf("%s executable hash mismatch", label)
	}
	return nil
}

// InputSHA256 returns the lowercase SHA-256 hex digest of input.
func InputSHA256(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

// UnwrapCodeFence strips a single surrounding markdown code fence when present.
func UnwrapCodeFence(s string) string {
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "```") {
		if idx := strings.IndexByte(trimmed, '\n'); idx >= 0 {
			inner := trimmed[idx+1:]
			if end := strings.LastIndex(inner, "```"); end >= 0 {
				return strings.TrimSpace(inner[:end])
			}
		}
	}
	return trimmed
}

// Truncate clips s to at most maxLen bytes and appends an ellipsis if clipped.
func Truncate(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// BuildResult constructs a normalized runtime.Result anchored to inv.Profile.
func BuildResult(inv runtime.Invocation, output string, usage runtime.Usage) runtime.Result {
	model := inv.Profile.Model
	provider := inv.Profile.Provider
	effort := inv.Profile.Effort
	return runtime.Result{
		Version:          1,
		InvocationID:     inv.ID,
		Requested:        inv.Profile,
		ObservedModel:    &model,
		ObservedProvider: &provider,
		ObservedEffort:   &effort,
		Output:           output,
		Usage:            usage,
	}
}

// InspectJournal replays a two-event runtime journal without side effects.
func InspectJournal[I, R any](path, intentKind, resultKind string) (*I, *R, string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil, "", nil
	}
	events, err := journal.Read(path)
	if err != nil {
		return nil, nil, "", err
	}
	var intent *I
	var receipt *R
	var head string
	for _, ev := range events {
		head = ev.Hash
		switch ev.Kind {
		case intentKind:
			var in I
			if err := canonical.Decode(ev.Payload, &in); err != nil {
				return nil, nil, "", err
			}
			intent = &in
		case resultKind:
			var rec R
			if err := canonical.Decode(ev.Payload, &rec); err != nil {
				return nil, nil, "", err
			}
			receipt = &rec
		}
	}
	return intent, receipt, head, nil
}

// ValidateTwoEventJournal validates one- or two-event intent/result journals.
func ValidateTwoEventJournal[I, R any](
	events []journal.Event,
	label, intentKind, resultKind string,
	intentID func(I) string,
	receiptID func(R) string,
) error {
	if len(events) == 0 || len(events) > 2 {
		return fmt.Errorf("invalid %s runtime event count", label)
	}
	if events[0].Kind != intentKind {
		return fmt.Errorf("%s runtime journal must start with intent", label)
	}
	var in I
	if err := canonical.Decode(events[0].Payload, &in); err != nil {
		return err
	}
	if len(events) == 2 {
		if events[1].Kind != resultKind {
			return fmt.Errorf("unexpected second %s runtime event", label)
		}
		var rec R
		if err := canonical.Decode(events[1].Payload, &rec); err != nil {
			return err
		}
		if receiptID(rec) != intentID(in) {
			return fmt.Errorf("%s receipt invocation mismatch", label)
		}
	}
	return nil
}

// CheckReplay checks an existing journal for a deterministic cached result or
// refuses unresolved duplicate invocations before any side effects occur.
func CheckReplay[I comparable, R any](
	journalPath, label, intentKind, resultKind string,
	wantIntent I,
	inv runtime.Invocation,
	receiptResult func(R) runtime.Result,
) (*runtime.Result, string, error) {
	intent, receipt, head, err := InspectJournal[I, R](journalPath, intentKind, resultKind)
	if err != nil {
		return nil, "", err
	}
	if intent == nil {
		return nil, "", nil
	}
	if *intent != wantIntent {
		return nil, "", fmt.Errorf("%s journal intent mismatch", label)
	}
	if receipt != nil {
		res := receiptResult(*receipt)
		if err := runtime.ValidateResult(inv, res, false); err != nil {
			return nil, "", err
		}
		return &res, head, nil
	}
	return nil, "", fmt.Errorf("%s invocation is unresolved; refusing to resend", label)
}

// AppendIntent creates the parent directory if needed and records the intent event.
func AppendIntent(journalPath, intentKind string, wantIntent any, validate func([]journal.Event) error) error {
	if err := os.MkdirAll(filepath.Dir(journalPath), 0o700); err != nil {
		return err
	}
	_, err := journal.Append(journalPath, intentKind, wantIntent, validate)
	return err
}

// PrepareInvocation checks replay state and records the intent event if absent.
func PrepareInvocation[I comparable, R any](
	journalPath, label, intentKind, resultKind string,
	wantIntent I,
	inv runtime.Invocation,
	validate func([]journal.Event) error,
	receiptResult func(R) runtime.Result,
) (*runtime.Result, string, error) {
	cached, head, err := CheckReplay[I, R](journalPath, label, intentKind, resultKind, wantIntent, inv, receiptResult)
	if err != nil || cached != nil {
		return cached, head, err
	}
	if err := AppendIntent(journalPath, intentKind, wantIntent, validate); err != nil {
		return nil, "", err
	}
	return nil, "", nil
}

// RecordResult validates res, hashes it under hashDomain, builds a receipt,
// and appends it to the runtime journal.
func RecordResult[R any](
	journalPath, resultKind, hashDomain string,
	inv runtime.Invocation,
	res runtime.Result,
	validate func([]journal.Event) error,
	buildReceipt func(resHash string) R,
) (runtime.Result, string, error) {
	if err := runtime.ValidateResult(inv, res, false); err != nil {
		return runtime.Result{}, "", err
	}
	resHash, err := canonical.Hash(hashDomain, res)
	if err != nil {
		return runtime.Result{}, "", err
	}
	rec := buildReceipt(resHash)
	ev, err := journal.Append(journalPath, resultKind, rec, validate)
	if err != nil {
		return runtime.Result{}, "", err
	}
	return res, ev.Hash, nil
}
