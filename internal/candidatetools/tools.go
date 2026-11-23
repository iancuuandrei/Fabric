// Package candidatetools defines and executes read-only tools for one exact
// mutable candidate observation. Callers must hold the workspace lease for the
// complete runtime execution and retain responsibility for request admission.
package candidatetools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/iancuuandrei/fabric/internal/anchoredit"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/sourcetools"
	"github.com/iancuuandrei/fabric/internal/toolcontent"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

const (
	// ListName identifies listing against the admitted candidate observation.
	ListName = "candidate_list"
	// ReadName identifies reading against the admitted candidate observation.
	ReadName = "candidate_read"
	// ValidateAnchoredEditsName validates a bounded edit proposal against the exact candidate.
	ValidateAnchoredEditsName = "candidate_validate_anchored_edits"
)

// AnchorValidationVersion opts an invocation into candidate-bound edit validation.
const AnchorValidationVersion = 1

// Binding pins mutable workspace reads to one exact admitted candidate.
type Binding struct {
	Workspace               worktree.Binding   `json:"workspace"`
	Candidate               worktree.Candidate `json:"candidate"`
	AnchorValidationVersion int                `json:"anchor_validation_version,omitempty"`
}

// Validate checks structural and source identity. Execute additionally
// reobserves the live candidate before making any bytes available.
func (b Binding) Validate(source repository.Identity) error {
	if b.AnchorValidationVersion != 0 && b.AnchorValidationVersion != AnchorValidationVersion {
		return errors.New("unsupported candidate anchor validation version")
	}
	id, err := b.Workspace.ID()
	if err != nil {
		return err
	}
	if err := b.Candidate.ValidateBinding(b.Workspace); err != nil {
		return err
	}
	if b.Workspace.Request.Source != source || b.Candidate.WorktreeID != id || b.Candidate.Head != source.Commit {
		return errors.New("runtime candidate/source binding mismatch")
	}
	return nil
}

// AnchoredEditArgs validates proposed changes to one existing candidate file.
type AnchoredEditArgs struct {
	CandidateID string            `json:"candidate_id"`
	Path        string            `json:"path"`
	BeforeHash  string            `json:"before_hash"`
	Edits       []anchoredit.Edit `json:"edits"`
}

// AnchoredEditValidation is safe, bounded diagnostic output; it never returns file contents.
type AnchoredEditValidation struct {
	CandidateID string `json:"candidate_id"`
	Path        string `json:"path"`
	BeforeHash  string `json:"before_hash"`
	Valid       bool   `json:"valid"`
	Diagnostic  string `json:"diagnostic,omitempty"`
	ResultHash  string `json:"result_hash,omitempty"`
}

// Page is a bounded, path-ordered view of the exact candidate manifest.
type Page struct {
	CandidateID string               `json:"candidate_id"`
	Files       []worktree.FileState `json:"files"`
	NextAfter   *string              `json:"next_after"`
}

// Catalog returns candidate tool definitions in stable order.
func Catalog() []sourcetools.Definition {
	return catalog(false)
}

// CatalogWithAnchoredEdits adds the opt-in read-only candidate validator.
func CatalogWithAnchoredEdits() []sourcetools.Definition {
	return catalog(true)
}

// LegacyCatalog returns the byte-identical candidate catalog used by v1
// context bindings.
func LegacyCatalog() []sourcetools.Definition {
	definitions := Catalog()
	definitions[1].Description = "Read a byte page from the admitted candidate, including modifications. Returns exact full-file hash and binary/UTF-8 views. Follow next_offset until null. Any candidate drift fails the request."
	return definitions
}

func catalog(includeAnchoredEdits bool) []sourcetools.Definition {
	definitions := []sourcetools.Definition{
		{
			Name:        ListName,
			Description: "List the exact admitted candidate's current regular files with hashes and modes. Use after empty initially, follow next_after until null. Unlike source_list this includes admitted modifications. Drift fails the request.",
			InputSchema: object(
				map[string]any{
					"after": map[string]any{"type": "string"},
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 128},
				},
				[]string{"after", "limit"},
			),
		},
		{
			Name:        ReadName,
			Description: "Read a byte page from the admitted candidate, including modifications. For valid UTF-8 pages, content_utf8 contains the bytes and content_base64 is omitted; binary pages include content_base64 and null content_utf8. sha256 binds the full candidate file. Follow next_offset until null. Any candidate drift fails the request.",
			InputSchema: object(
				map[string]any{
					"path":   map[string]any{"type": "string"},
					"offset": map[string]any{"type": "integer", "minimum": 0},
					"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 32768},
				},
				[]string{"path", "offset", "limit"},
			),
		},
	}
	if includeAnchoredEdits {
		definitions = append(definitions, sourcetools.Definition{
			Name:        ValidateAnchoredEditsName,
			Description: "Read-only validation of anchored replacements against one exact existing file in the admitted candidate. Returns validity and a result hash, never file contents. A false result is diagnostic only; final host admission still validates independently.",
			InputSchema: object(map[string]any{
				"candidate_id": map[string]any{"type": "string"},
				"path":         map[string]any{"type": "string"},
				"before_hash":  map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
				"edits":        map[string]any{"type": "array", "minItems": 1, "maxItems": anchoredit.MaxEdits, "items": object(map[string]any{"before": map[string]any{"type": "string", "minLength": 1}, "after": map[string]any{"type": "string"}}, []string{"before", "after"})},
			}, []string{"candidate_id", "path", "before_hash", "edits"}),
		})
	}
	return definitions
}

func object(properties map[string]any, required []string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

// ListArgs are the bounded candidate_list arguments.
type ListArgs struct {
	After string `json:"after"`
	Limit int    `json:"limit"`
}

// ReadArgs are the bounded candidate_read arguments.
type ReadArgs struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Limit  int    `json:"limit"`
}

// ReadResult is the model-facing view of an admitted candidate page. Text
// pages omit their redundant Base64 copy; binary pages retain Base64.
type ReadResult struct {
	CandidateID   string  `json:"candidate_id"`
	Path          string  `json:"path"`
	SHA256        string  `json:"sha256"`
	Size          int64   `json:"size"`
	Offset        int64   `json:"offset"`
	ContentBase64 string  `json:"content_base64,omitempty"`
	ContentUTF8   *string `json:"content_utf8"`
	NextOffset    *int64  `json:"next_offset"`
}

func readResult(chunk worktree.SourceChunk) (ReadResult, error) {
	encoded, text, err := toolcontent.UTF8First(chunk.ContentBase64, chunk.ContentUTF8)
	if err != nil {
		return ReadResult{}, err
	}
	return ReadResult{chunk.CandidateID, chunk.Path, chunk.SHA256, chunk.Size, chunk.Offset, encoded, text, chunk.NextOffset}, nil
}

// Execute strictly decodes and executes one supported candidate tool. Unknown
// names are returned as unhandled without inspecting the binding or workspace.
func Execute(ctx context.Context, binding Binding, name string, arguments json.RawMessage) (content any, handled bool, err error) {
	return execute(ctx, binding, name, arguments, true)
}

// ExecuteLegacy retains the v1 candidate-read result shape for sessions whose
// model was given the v1 tool catalog.
func ExecuteLegacy(ctx context.Context, binding Binding, name string, arguments json.RawMessage) (content any, handled bool, err error) {
	return execute(ctx, binding, name, arguments, false)
}

func execute(ctx context.Context, binding Binding, name string, arguments json.RawMessage, utf8First bool) (content any, handled bool, err error) {
	switch name {
	case ListName:
		var args ListArgs
		if err := canonical.Decode(arguments, &args); err != nil {
			return nil, true, err
		}
		if args.Limit < 1 || args.Limit > 128 || len(args.After) > 4096 {
			return nil, true, errors.New("invalid candidate list bounds")
		}
		if err := binding.Validate(binding.Workspace.Request.Source); err != nil {
			return nil, true, err
		}
		content, err := list(ctx, binding, args.After, args.Limit)
		return content, true, err
	case ReadName:
		var args ReadArgs
		if err := canonical.Decode(arguments, &args); err != nil {
			return nil, true, err
		}
		if args.Offset < 0 || args.Offset > 64<<20 || args.Limit < 1 || args.Limit > 32768 {
			return nil, true, errors.New("invalid candidate read bounds")
		}
		if err := binding.Validate(binding.Workspace.Request.Source); err != nil {
			return nil, true, err
		}
		chunk, err := worktree.ReadSource(ctx, binding.Workspace, binding.Candidate, args.Path, args.Offset, args.Limit)
		if err != nil {
			return nil, true, err
		}
		if !utf8First {
			return chunk, true, nil
		}
		result, err := readResult(chunk)
		if err != nil {
			return nil, true, err
		}
		return result, true, nil
	case ValidateAnchoredEditsName:
		if binding.AnchorValidationVersion != AnchorValidationVersion {
			return nil, true, errors.New("candidate anchored edit validation is not enabled")
		}
		var args AnchoredEditArgs
		if err := canonical.Decode(arguments, &args); err != nil {
			return nil, true, err
		}
		if err := binding.Validate(binding.Workspace.Request.Source); err != nil {
			return nil, true, err
		}
		result, err := validateAnchoredEdits(ctx, binding, args)
		return result, true, err
	default:
		return nil, false, nil
	}
}

func validateAnchoredEdits(ctx context.Context, binding Binding, args AnchoredEditArgs) (AnchoredEditValidation, error) {
	if err := binding.Candidate.ValidateBinding(binding.Workspace); err != nil {
		return AnchoredEditValidation{}, err
	}
	candidateID, err := binding.Candidate.ID()
	if err != nil {
		return AnchoredEditValidation{}, err
	}
	if args.CandidateID != candidateID {
		return AnchoredEditValidation{}, errors.New("candidate validation identity mismatch")
	}
	if err := safepath.Writable(args.Path); err != nil {
		return AnchoredEditValidation{}, err
	}
	if err := safepath.RequireDigest(args.BeforeHash); err != nil {
		return AnchoredEditValidation{}, errors.New("invalid candidate file hash")
	}
	captured, files, err := worktree.Capture(ctx, binding.Workspace)
	if err != nil {
		return AnchoredEditValidation{}, err
	}
	if captured != binding.Candidate {
		return AnchoredEditValidation{}, errors.New("candidate changed before edit validation")
	}
	var file *worktree.FileState
	for i := range files {
		if files[i].Path == args.Path {
			file = &files[i]
			break
		}
	}
	if file == nil || file.Hash != args.BeforeHash {
		return AnchoredEditValidation{}, errors.New("candidate file hash mismatch")
	}
	var source []byte
	for offset := int64(0); ; {
		chunk, readErr := worktree.ReadSource(ctx, binding.Workspace, binding.Candidate, args.Path, offset, 32768)
		if readErr != nil {
			return AnchoredEditValidation{}, readErr
		}
		if chunk.CandidateID != candidateID || chunk.Path != args.Path || chunk.SHA256 != args.BeforeHash || chunk.Offset != offset || chunk.Size > anchoredit.MaxSourceBytes {
			return AnchoredEditValidation{}, errors.New("candidate source observation mismatch")
		}
		// ReadSource returns canonical standard base64; reject noncanonical encodings.
		pageBytes, decodeErr := decodeCandidatePage(chunk.ContentBase64)
		if decodeErr != nil {
			return AnchoredEditValidation{}, decodeErr
		}
		if len(source)+len(pageBytes) > anchoredit.MaxSourceBytes || len(pageBytes) == 0 && chunk.NextOffset != nil {
			return AnchoredEditValidation{}, errors.New("candidate source size outside validation bound")
		}
		source = append(source, pageBytes...)
		if chunk.NextOffset == nil {
			if int64(len(source)) != chunk.Size {
				return AnchoredEditValidation{}, errors.New("candidate source page sequence incomplete")
			}
			break
		}
		if *chunk.NextOffset != offset+int64(len(pageBytes)) || len(pageBytes) != 32768 {
			return AnchoredEditValidation{}, errors.New("candidate source page sequence invalid")
		}
		offset = *chunk.NextOffset
	}
	result := AnchoredEditValidation{CandidateID: candidateID, Path: args.Path, BeforeHash: args.BeforeHash}
	composed, applyErr := anchoredit.Apply(source, args.Edits)
	if applyErr != nil {
		result.Diagnostic = boundedDiagnostic(applyErr)
		return result, nil
	}
	digest := sha256.Sum256(composed)
	result.Valid, result.ResultHash = true, hex.EncodeToString(digest[:])
	return result, nil
}

func decodeCandidatePage(encoded string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != encoded {
		return nil, errors.New("invalid candidate source page encoding")
	}
	return decoded, nil
}

func boundedDiagnostic(err error) string {
	message := strings.TrimSpace(err.Error())
	if len(message) > 240 {
		message = message[:240]
	}
	if message == "" {
		return "anchored edit proposal is invalid"
	}
	return message
}

func list(ctx context.Context, binding Binding, after string, limit int) (Page, error) {
	if limit < 1 || limit > 128 || len(after) > 4096 {
		return Page{}, errors.New("invalid candidate list bounds")
	}
	candidate, files, err := worktree.Capture(ctx, binding.Workspace)
	if err != nil {
		return Page{}, err
	}
	if candidate != binding.Candidate {
		return Page{}, errors.New("candidate changed before listing")
	}
	id, err := candidate.ID()
	if err != nil {
		return Page{}, err
	}
	page := Page{CandidateID: id, Files: []worktree.FileState{}}
	for _, file := range files {
		if file.Path <= after {
			continue
		}
		if len(page.Files) == limit {
			last := page.Files[len(page.Files)-1].Path
			page.NextAfter = &last
			break
		}
		page.Files = append(page.Files, file)
	}
	return page, nil
}
