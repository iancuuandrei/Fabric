package faultlocalization

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/safepath"
)

// MaxEncodedBytes bounds a complete imported spectrum, including all profiles.
const MaxEncodedBytes = 1 << 20

// Source maps a Go profile filename to exact candidate file bytes. The mapping
// is a caller assertion; a matching hash cannot authenticate coverage production.
type Source struct {
	Path        string `json:"path"`
	ProfilePath string `json:"profile_path"`
	SHA256      string `json:"sha256"`
}

// Test carries one individual test's complete profile and asserted PASS/FAIL.
// Combined-suite profiles cannot establish which individual test covered a block.
type Test struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome"`
	Profile string `json:"profile"`
}

// Spectrum is a bounded untrusted artifact. Run/candidate bindings prevent
// accidental reuse; they do not authenticate test outcomes or source mappings.
type Spectrum struct {
	Version     int      `json:"version"`
	RunID       string   `json:"run_id"`
	CandidateID string   `json:"candidate_id"`
	Sources     []Source `json:"sources"`
	Tests       []Test   `json:"tests"`
}

// Block retains raw per-test counts and the exact square of Ochiai's score.
// Score is sqrt(numerator/denominator); the fraction avoids floating canonical
// output and preserves exact comparisons. Zero denominator means not executed.
type Block struct {
	Path            string `json:"path"`
	StartLine       int    `json:"start_line"`
	StartColumn     int    `json:"start_column"`
	EndLine         int    `json:"end_line"`
	EndColumn       int    `json:"end_column"`
	Statements      int    `json:"statements"`
	FailedExecuting int    `json:"failed_executing"`
	PassedExecuting int    `json:"passed_executing"`
	Numerator       int    `json:"ochiai_squared_numerator"`
	Denominator     int    `json:"ochiai_squared_denominator"`
}

// Report is an advisory ranking, independently bracketed by controller source
// observation before publication. It cannot alter findings, ownership or gates.
type Report struct {
	Version       int     `json:"version"`
	InputHash     string  `json:"input_hash"`
	RunID         string  `json:"run_id"`
	CandidateID   string  `json:"candidate_id"`
	Provenance    string  `json:"provenance"`
	SourceStatus  string  `json:"source_status"`
	FailedTests   int     `json:"failed_tests"`
	PassedTests   int     `json:"passed_tests"`
	Blocks        []Block `json:"blocks"`
	OmittedBlocks int     `json:"omitted_blocks,omitempty"`
}

// Analyze validates bounded complete block inventories and ranks by Ochiai.
// It performs no I/O. UNKNOWN, missing tests, duplicate IDs/blocks, inconsistent
// instrumentation and mixed coverage modes are rejected rather than inferred.
func Analyze(s Spectrum) (Report, error) {
	r := Report{Version: 1, RunID: s.RunID, CandidateID: s.CandidateID,
		Provenance: "caller_supplied_untrusted_test_spectra", SourceStatus: "not_observed", Blocks: []Block{}}
	sources, err := validateSpectrum(s)
	if err != nil {
		return r, err
	}
	r.InputHash, err = canonical.Hash("fabric.repair-spectrum.v1", s)
	if err != nil {
		return r, err
	}
	blocks, failed, passed, err := aggregateProfiles(s.Tests, sources)
	if err != nil {
		return r, err
	}
	r.FailedTests, r.PassedTests = failed, passed
	r.Blocks, err = rankBlocks(blocks, failed, len(s.Sources))
	return r, err
}

func validateSpectrum(s Spectrum) (map[string]string, error) {
	if s.Version != 1 || !hashValid(s.RunID) || !hashValid(s.CandidateID) || len(s.Sources) == 0 || len(s.Sources) > 24 || len(s.Tests) == 0 || len(s.Tests) > 64 {
		return nil, errors.New("invalid spectrum binding or bounds")
	}
	// Validate every string field and aggregate raw bounds before JSON allocation.
	// Escaping can expand this bounded input; the exact encoded cap follows.
	sources, err := spectrumSources(s.Sources)
	if err != nil {
		return nil, err
	}
	if err := spectrumTestBounds(s.Tests); err != nil {
		return nil, err
	}
	raw, err := canonical.Bytes(s)
	if err != nil || len(raw) > MaxEncodedBytes {
		return nil, errors.New("spectrum exceeds encoded bounds")
	}
	return sources, nil
}

func aggregateProfiles(tests []Test, sources map[string]string) (map[string]Block, int, int, error) {
	mode := ""
	var blocks map[string]Block
	failed, passed := 0, 0
	for _, test := range tests {
		observed, profileMode, err := parseProfile(test.Profile, sources)
		if err != nil {
			return nil, 0, 0, err
		}
		if mode != "" && mode != profileMode {
			return nil, 0, 0, errors.New("spectrum coverage modes differ")
		}
		mode = profileMode
		blocks, err = mergeProfile(blocks, observed, test.Outcome)
		if err != nil {
			return nil, 0, 0, err
		}
		if test.Outcome == "FAIL" {
			failed++
		} else {
			passed++
		}
	}
	if failed == 0 {
		return nil, 0, 0, errors.New("spectrum has no failing tests")
	}
	return blocks, failed, passed, nil
}

func mergeProfile(blocks map[string]Block, observed map[string]observation, outcome string) (map[string]Block, error) {
	if blocks == nil {
		blocks = make(map[string]Block, len(observed))
		for key, b := range observed {
			blocks[key] = b.Block
		}
	}
	if len(blocks) != len(observed) {
		return nil, errors.New("spectrum block inventories differ")
	}
	for key, b := range observed {
		total, ok := blocks[key]
		if !ok || total.Statements != b.Statements {
			return nil, errors.New("spectrum block inventories differ")
		}
		if b.executed {
			total.FailedExecuting += boolCount(outcome == "FAIL")
			total.PassedExecuting += boolCount(outcome == "PASS")
		}
		blocks[key] = total
	}
	return blocks, nil
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func rankBlocks(blocks map[string]Block, failed, sourceCount int) ([]Block, error) {
	result := make([]Block, 0, len(blocks))
	coveredPaths := map[string]bool{}
	for _, b := range blocks {
		coveredPaths[b.Path] = true
		b.Numerator = b.FailedExecuting * b.FailedExecuting
		b.Denominator = failed * (b.FailedExecuting + b.PassedExecuting)
		result = append(result, b)
	}
	if len(coveredPaths) != sourceCount {
		return nil, errors.New("spectrum declares uncovered source mappings")
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		// At most 64 tests: cross products are bounded well below int64.
		left, right := int64(a.Numerator)*int64(max(b.Denominator, 1)), int64(b.Numerator)*int64(max(a.Denominator, 1))
		if left != right {
			return left > right
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.StartLine != b.StartLine {
			return a.StartLine < b.StartLine
		}
		if a.StartColumn != b.StartColumn {
			return a.StartColumn < b.StartColumn
		}
		if a.EndLine != b.EndLine {
			return a.EndLine < b.EndLine
		}
		return a.EndColumn < b.EndColumn
	})
	return result, nil
}

func spectrumSources(input []Source) (map[string]string, error) {
	sources, paths := map[string]string{}, map[string]bool{}
	for _, f := range input {
		if safepath.Writable(f.Path) != nil || safepath.Relative(f.ProfilePath) != nil || !hashValid(f.SHA256) || sources[f.ProfilePath] != "" || paths[f.Path] {
			return nil, errors.New("invalid or duplicate spectrum source")
		}
		sources[f.ProfilePath], paths[f.Path] = f.Path, true
	}
	return sources, nil
}

func spectrumTestBounds(tests []Test) error {
	bytes := 0
	seen := map[string]bool{}
	for _, test := range tests {
		if !testIdentityValid(test) || seen[test.ID] || len(test.Profile) == 0 || len(test.Profile) > 64<<10 {
			return errors.New("spectrum test exceeds field bounds")
		}
		seen[test.ID] = true
		bytes += len(test.Profile) + len(test.ID)
		if bytes > MaxEncodedBytes {
			return errors.New("spectrum tests exceed aggregate bounds")
		}
	}
	return nil
}

func testIdentityValid(test Test) bool {
	return test.ID != "" && len(test.ID) <= 128 && utf8.ValidString(test.ID) && strings.IndexFunc(test.ID, func(c rune) bool { return c < 32 || c == 127 }) < 0 && (test.Outcome == "PASS" || test.Outcome == "FAIL")
}

type observation struct {
	Block
	executed bool
}

func parseProfile(profile string, sources map[string]string) (map[string]observation, string, error) {
	lines, mode, err := profileLines(profile)
	if err != nil {
		return nil, "", err
	}
	result := map[string]observation{}
	for _, line := range lines {
		key, b, err := parseProfileBlock(line, sources, mode)
		if err != nil {
			return nil, "", err
		}
		if _, exists := result[key]; exists {
			return nil, "", errors.New("duplicate Go coverage block")
		}
		result[key] = b
	}
	return result, mode, nil
}

func profileLines(profile string) ([]string, string, error) {
	invalid := errors.New("invalid or incomplete Go coverage profile")
	if len(profile) == 0 || len(profile) > 64<<10 || !utf8.ValidString(profile) || strings.ContainsRune(profile, 0) {
		return nil, "", invalid
	}
	lines := strings.Split(strings.TrimSuffix(profile, "\n"), "\n")
	if len(lines) < 2 || len(lines) > 4097 {
		return nil, "", invalid
	}
	header := strings.Fields(lines[0])
	if len(header) != 2 || header[0] != "mode:" || (header[1] != "set" && header[1] != "count" && header[1] != "atomic") {
		return nil, "", invalid
	}
	return lines[1:], header[1], nil
}

func parseProfileBlock(line string, sources map[string]string, mode string) (string, observation, error) {
	invalid := errors.New("invalid Go coverage block")
	colon := strings.LastIndexByte(line, ':')
	if colon <= 0 {
		return "", observation{}, invalid
	}
	path := sources[line[:colon]]
	if path == "" {
		return "", observation{}, invalid
	}
	fields := strings.Fields(line[colon+1:])
	if len(fields) != 3 {
		return "", observation{}, invalid
	}
	coords := strings.Split(fields[0], ",")
	if len(coords) != 2 {
		return "", observation{}, invalid
	}
	b, err := profileBlockNumbers(coords, fields[1], fields[2], mode)
	if err != nil {
		return "", observation{}, err
	}
	b.Path = path
	key := path + ":" + strconv.Itoa(b.StartLine) + "." + strconv.Itoa(b.StartColumn) + "," + strconv.Itoa(b.EndLine) + "." + strconv.Itoa(b.EndColumn)
	return key, b, nil
}

func profileBlockNumbers(coords []string, statementText, countText, mode string) (observation, error) {
	sl, sc, ok := position(coords[0])
	el, ec, endOK := position(coords[1])
	statements, e1 := strconv.Atoi(statementText)
	count, e2 := strconv.ParseUint(countText, 10, 64)
	if !ok || !endOK || !rangeOrdered(sl, sc, el, ec) || e1 != nil || statements < 0 || statements > 1<<20 || e2 != nil || !counterValid(mode, count) {
		return observation{}, errors.New("invalid Go coverage coordinates or counter")
	}
	return observation{Block: Block{StartLine: sl, StartColumn: sc, EndLine: el, EndColumn: ec, Statements: statements}, executed: count > 0}, nil
}

func rangeOrdered(sl, sc, el, ec int) bool        { return el > sl || el == sl && ec > sc }
func counterValid(mode string, count uint64) bool { return mode != "set" || count <= 1 }

func position(s string) (int, int, bool) {
	p := strings.Split(s, ".")
	if len(p) != 2 {
		return 0, 0, false
	}
	l, e1 := strconv.Atoi(p[0])
	c, e2 := strconv.Atoi(p[1])
	return l, c, e1 == nil && e2 == nil && l > 0 && c > 0 && l <= 1<<20 && c <= 1<<20
}

func hashValid(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}
