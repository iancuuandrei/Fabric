package ri

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"harness.local/engorch/internal/canonical"
)

// ReadLexicalRecords hydrates a controller-selected JSONL manifest, verifying
// exact source/identity, canonical records, strict path order and finite bounds.
// It returns no partial scope on failure and does not establish Git membership.
func ReadLexicalRecords(ctx context.Context, input io.Reader, expected Source, expectedID string) (LexicalManifest, error) {
	if err := ctx.Err(); err != nil {
		return LexicalManifest{}, err
	}
	if input == nil {
		return LexicalManifest{}, errors.New("manifest reader required")
	}
	reader := bufio.NewScanner(io.LimitReader(input, (512<<20)+1))
	reader.Buffer(make([]byte, 4096), canonical.MaxBytes+2)
	reader.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if n := bytes.IndexByte(data, '\n'); n >= 0 {
			return n + 1, data[:n], nil
		}
		if atEOF && len(data) > 0 {
			return 0, nil, errors.New("torn lexical manifest record")
		}
		return 0, nil, nil
	})
	manifest := LexicalManifest{Version: 1, Files: []LexicalFile{}}
	seenSource := false
	total := 0
	fail := func(err error) (LexicalManifest, error) { return LexicalManifest{}, err }
	for reader.Scan() {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		raw := reader.Bytes()
		total += len(raw) + 1
		if total > 512<<20 || len(raw) > canonical.MaxBytes {
			return fail(errors.New("lexical manifest transport bound exceeded"))
		}
		normal, err := canonical.Normalize(raw)
		if err != nil {
			return fail(err)
		}
		if !bytes.Equal(normal, raw) {
			return fail(errors.New("noncanonical lexical record"))
		}
		var record struct {
			Kind  string          `json:"kind"`
			Value json.RawMessage `json:"value"`
		}
		if err := canonical.Decode(raw, &record); err != nil {
			return fail(err)
		}
		switch record.Kind {
		case "source":
			if seenSource {
				return fail(errors.New("duplicate lexical source"))
			}
			if err := canonical.Decode(record.Value, &manifest.Source); err != nil {
				return fail(err)
			}
			if manifest.Source != expected {
				return fail(errors.New("lexical source differs"))
			}
			seenSource = true
		case "file":
			if !seenSource || len(manifest.Files) >= 500000 {
				return fail(errors.New("lexical file order or count invalid"))
			}
			var file LexicalFile
			if err := canonical.Decode(record.Value, &file); err != nil {
				return fail(err)
			}
			if len(manifest.Files) > 0 && manifest.Files[len(manifest.Files)-1].Path >= file.Path {
				return fail(errors.New("lexical paths not strictly ordered"))
			}
			manifest.Files = append(manifest.Files, file)
		default:
			return fail(errors.New("unknown lexical record"))
		}
	}
	if err := reader.Err(); err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if !seenSource {
		return fail(errors.New("lexical source missing"))
	}
	id, err := manifest.ID()
	if err != nil {
		return fail(err)
	}
	if id != expectedID {
		return fail(errors.New("lexical manifest identity differs"))
	}
	return manifest, nil
}
