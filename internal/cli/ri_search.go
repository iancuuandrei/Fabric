package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/ri"
	"io"
)

func riSearchCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) < 5 {
		return errors.New("usage: ri search EXE EXE_SHA256 REF_JSON [--fixed|--regex] [--case-insensitive] [--path PATH] [--type EXT] [--limit N] [--after CURSOR_JSON] [--overlay-run RUN] [--json] [--explain] PATTERN")
	}
	executable, err := riAbsolutePath(root, args[1])
	if err != nil {
		return err
	}
	refPath, err := riAbsolutePath(root, args[3])
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("ri search", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	fixed := flags.Bool("fixed", false, "literal matching")
	regex := flags.Bool("regex", false, "regex matching (default)")
	insensitive := flags.Bool("case-insensitive", false, "Unicode case-insensitive matching")
	pathFilter := flags.String("path", "", "exact repository path or subtree")
	typeFilter := flags.String("type", "", "case-sensitive file extension without a dot")
	limit := flags.Int("limit", 100, "maximum exact hits")
	after := flags.String("after", "", "bound continuation JSON")
	overlayRun := flags.String("overlay-run", "", "confirmed overlay for the current admitted run candidate")
	jsonOutput := flags.Bool("json", false, "emit JSON (the only supported output format)")
	explain := flags.Bool("explain", false, "include Rust candidate-plan evidence (always present)")
	if err := flags.Parse(args[4:]); err != nil {
		return err
	}
	if flags.NArg() != 1 || (*fixed && *regex) {
		return errors.New("one pattern and mutually exclusive fixed/regex modes required")
	}
	// JSON output and Rust-derived planning fields are unconditional. These
	// explicit flags make that contract discoverable without changing its shape.
	_ = *jsonOutput
	_ = *explain
	var ref ri.LexicalRef
	var raw json.RawMessage
	if err := readJSON(refPath, &raw); err != nil {
		return err
	}
	var compact ri.LexicalArtifactRef
	compactErr := canonical.Decode(raw, &compact)
	if compactErr != nil {
		if err := canonical.Decode(raw, &ref); err != nil {
			return errors.New("invalid compact or legacy lexical reference")
		}
	}
	cfg, err := configuration(root)
	if err != nil {
		return err
	}
	identity, err := repository.Discover(ctx, root, cfg.Repository)
	if err != nil {
		return err
	}
	source, err := ri.FromRepository(identity)
	if err != nil {
		return err
	}
	if compactErr == nil {
		if compact.Manifest.Source != source {
			return errors.New("compact lexical reference differs from current committed source")
		}
		ref, err = compact.Hydrate(ctx)
		if err != nil {
			return err
		}
	}
	if source != ref.Manifest.Source {
		return errors.New("lexical reference differs from current committed source")
	}
	var cursor *ri.LexicalCursor
	if *after != "" {
		cursor = &ri.LexicalCursor{}
		if err := canonical.Decode([]byte(*after), cursor); err != nil {
			return err
		}
	}
	client := ri.Client{Executable: executable, ExecutableHash: args[2]}
	query := ri.LexicalQuery{Pattern: flags.Arg(0), Fixed: *fixed, CaseInsensitive: *insensitive, Path: *pathFilter, Type: *typeFilter}
	if *overlayRun != "" {
		path, err := runPath(root, *overlayRun)
		if err != nil {
			return err
		}
		s, err := control.Inspect(path)
		if err != nil {
			return err
		}
		if s.RunID != *overlayRun || s.Creation.Repository != identity || s.RILexicalOverlay == nil {
			return errors.New("overlay run repository binding mismatch")
		}
		baseID, err := ref.Manifest.ID()
		if err != nil {
			return err
		}
		if baseID != s.RILexicalOverlay.Intent.Prepared.Plan.BaseID {
			return errors.New("overlay base differs from search reference")
		}
		overlay, err := control.SelectLexicalOverlay(ctx, path)
		if err != nil {
			return err
		}
		result, err := client.SearchLexicalOverlay(ctx, ref, overlay, query, *limit, cursor)
		if err != nil {
			return err
		}
		return output(out, result)
	}
	result, err := client.SearchLexical(ctx, ref, query, *limit, cursor)
	if err != nil {
		return err
	}
	return output(out, result)
}
