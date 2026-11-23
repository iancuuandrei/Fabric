package faultlocalization

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

func fixture() Spectrum {
	return Spectrum{Version: 1, RunID: strings.Repeat("a", 64), CandidateID: strings.Repeat("b", 64),
		Sources: []Source{{Path: "p.go", ProfilePath: "example.invalid/p.go", SHA256: strings.Repeat("c", 64)}},
		Tests: []Test{
			{ID: "fail", Outcome: "FAIL", Profile: "mode: set\nexample.invalid/p.go:1.1,1.2 1 1\nexample.invalid/p.go:2.1,2.2 1 1\nexample.invalid/p.go:3.1,3.2 1 0\n"},
			{ID: "pass", Outcome: "PASS", Profile: "mode: set\nexample.invalid/p.go:3.1,3.2 1 0\nexample.invalid/p.go:2.1,2.2 1 1\nexample.invalid/p.go:1.1,1.2 1 0\n"}}}
}

func TestSpectrumExactOchiaiAndInventoryOrder(t *testing.T) {
	r, err := Analyze(fixture())
	if err != nil || len(r.Blocks) != 3 {
		t.Fatal(r, err)
	}
	if r.Blocks[0].StartLine != 1 || r.Blocks[0].Numerator != 1 || r.Blocks[0].Denominator != 1 || r.Blocks[1].Denominator != 2 || r.Blocks[2].Denominator != 0 {
		t.Fatal("wrong exact scores", r)
	}
	if r.FailedTests != 1 || r.PassedTests != 1 || r.Blocks[0].PassedExecuting != 0 || r.Blocks[1].PassedExecuting != 1 {
		t.Fatal("lost raw counts", r)
	}
	if _, err := canonical.Bytes(r); err != nil {
		t.Fatal("noncanonical rank", err)
	}
	s := fixture()
	s.Tests[0].Profile = strings.ReplaceAll(s.Tests[0].Profile, " 1 1", " 1 0")
	changed, err := Analyze(s)
	if err != nil || changed.InputHash == r.InputHash {
		t.Fatal("evidence change lost identity", err)
	}
	// Equal ranks use deterministic source position, never map iteration.
	for i := 0; i < 10; i++ {
		same, _ := Analyze(fixture())
		a, _ := canonical.Bytes(same)
		b, _ := canonical.Bytes(r)
		if string(a) != string(b) {
			t.Fatal("unstable ranking")
		}
	}
}

func TestSpectrumRejectsUncertainIncompleteDuplicateAndForeignEvidence(t *testing.T) {
	for name, change := range map[string]func(*Spectrum){
		"unknown":            func(s *Spectrum) { s.Tests[0].Outcome = "UNKNOWN" },
		"missing failed":     func(s *Spectrum) { s.Tests[0].Outcome = "PASS" },
		"duplicate test":     func(s *Spectrum) { s.Tests[1].ID = s.Tests[0].ID },
		"duplicate block":    func(s *Spectrum) { s.Tests[0].Profile += "example.invalid/p.go:1.1,1.2 1 1\n" },
		"numeric duplicate":  func(s *Spectrum) { s.Tests[0].Profile += "example.invalid/p.go:01.1,1.2 1 1\n" },
		"missing block":      func(s *Spectrum) { s.Tests[1].Profile = "mode: set\nexample.invalid/p.go:1.1,1.2 1 0\n" },
		"statement mismatch": func(s *Spectrum) { s.Tests[1].Profile = strings.ReplaceAll(s.Tests[1].Profile, " 1 1", " 2 1") },
		"foreign file":       func(s *Spectrum) { s.Tests[0].Profile = strings.ReplaceAll(s.Tests[0].Profile, "p.go:", "other.go:") },
		"mixed mode": func(s *Spectrum) {
			s.Tests[0].Profile = strings.Replace(s.Tests[0].Profile, "mode: set", "mode: count", 1)
		},
		"set counter":         func(s *Spectrum) { s.Tests[0].Profile = strings.ReplaceAll(s.Tests[0].Profile, " 1 1", " 1 2") },
		"negative count":      func(s *Spectrum) { s.Tests[0].Profile = strings.ReplaceAll(s.Tests[0].Profile, " 1 1", " 1 -1") },
		"reverse range":       func(s *Spectrum) { s.Tests[0].Profile = strings.ReplaceAll(s.Tests[0].Profile, "1.1,1.2", "1.2,1.1") },
		"unsafe source":       func(s *Spectrum) { s.Sources[0].Path = "../p.go" },
		"protected source":    func(s *Spectrum) { s.Sources[0].Path = ".harness/p.go" },
		"unsafe profile path": func(s *Spectrum) { s.Sources[0].ProfilePath = "C:/p.go" },
		"hash":                func(s *Spectrum) { s.Sources[0].SHA256 = "not-a-hash" },
		"run":                 func(s *Spectrum) { s.RunID = "foreign" },
		"NUL":                 func(s *Spectrum) { s.Tests[0].Profile += "\x00" },
		"oversize profile":    func(s *Spectrum) { s.Tests[0].Profile = strings.Repeat("x", (64<<10)+1) },
		"oversize outcome":    func(s *Spectrum) { s.Tests[0].Outcome = strings.Repeat("x", (1<<20)+1) },
		"oversize source":     func(s *Spectrum) { s.Sources[0].Path = strings.Repeat("x", (1<<20)+1) },
		"test count":          func(s *Spectrum) { s.Tests = make([]Test, 65) },
		"source count":        func(s *Spectrum) { s.Sources = make([]Source, 25) },
	} {
		t.Run(name, func(t *testing.T) {
			s := fixture()
			change(&s)
			if _, err := Analyze(s); err == nil {
				t.Fatal("invalid evidence ranked")
			}
		})
	}
}

func TestSpectrumPreflightBoundsBeforeCanonicalAllocation(t *testing.T) {
	for _, test := range []Test{{ID: "t", Outcome: strings.Repeat("x", (1<<20)+1), Profile: "valid-size"}, {ID: "t", Outcome: "FAIL", Profile: strings.Repeat("x", (64<<10)+1)}} {
		if err := spectrumTestBounds([]Test{test}); err == nil {
			t.Fatal("oversized direct-call field passed preflight")
		}
	}
	s := fixture()
	s.Tests = nil
	for i := 0; i < 4; i++ {
		s.Tests = append(s.Tests, Test{ID: string(rune('a' + i)), Outcome: "FAIL", Profile: strings.Repeat("\x01", 64<<10)})
	}
	if _, err := Analyze(s); err == nil {
		t.Fatal("escaped encoded overflow accepted")
	}
}

func TestSpectrumCountsTestsNotVisitsAndSupportsSpacesAndZeroStatements(t *testing.T) {
	s := fixture()
	for i := range s.Tests {
		s.Tests[i].Profile = strings.ReplaceAll(s.Tests[i].Profile, "mode: set", "mode: atomic")
		s.Tests[i].Profile = strings.ReplaceAll(s.Tests[i].Profile, " 1 1", " 0 900000")
		s.Tests[i].Profile = strings.ReplaceAll(s.Tests[i].Profile, " 1 0", " 0 0")
		s.Tests[i].Profile = strings.ReplaceAll(s.Tests[i].Profile, "p.go:", "p file.go:")
	}
	s.Sources[0].ProfilePath = "example.invalid/p file.go"
	r, err := Analyze(s)
	if err != nil || r.Blocks[0].FailedExecuting != 1 || r.Blocks[1].PassedExecuting != 1 {
		t.Fatal("visits became independent tests", r, err)
	}
}

func TestSpectrumRealIndividualGoCoverage(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{
		"go.mod":    "module example.invalid/spectrum\n\ngo 1.24\n",
		"p.go":      "package spectrum\nfunc Pick(x int) int {\n if x == 0 { return 1 }\n if x > 0 { return 2 }\n return 3\n}\n",
		"p_test.go": "package spectrum\nimport \"testing\"\nfunc TestFailure(t *testing.T) { if Pick(0)!=0 { t.Fatal(\"wrong zero result\") } }\nfunc TestPassing(t *testing.T) { if Pick(1)!=2 { t.Fatal(\"wrong positive result\") } }\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goTool += ".exe"
	}
	s := fixture()
	s.Sources[0].Path = "p.go"
	s.Sources[0].ProfilePath = "example.invalid/spectrum/p.go"
	s.Tests = nil
	for _, name := range []string{"TestFailure", "TestPassing"} {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		cmd := exec.CommandContext(ctx, goTool, "test", "-run", "^"+name+"$", "-count=1", "-covermode=set", "-coverprofile="+name+".out", ".")
		cmd.Dir = dir
		output, err := cmd.CombinedOutput()
		cancel()
		outcome := "PASS"
		if name == "TestFailure" {
			outcome = "FAIL"
		}
		if (err == nil) != (outcome == "PASS") {
			t.Fatalf("unexpected native outcome: %v %s", err, output)
		}
		profile, err := os.ReadFile(filepath.Join(dir, name+".out"))
		if err != nil {
			t.Fatal(err)
		}
		s.Tests = append(s.Tests, Test{ID: name, Outcome: outcome, Profile: string(profile)})
	}
	r, err := Analyze(s)
	if err != nil || r.FailedTests != 1 || r.PassedTests != 1 || len(r.Blocks) == 0 {
		t.Fatal("actual profiles not usable", r, err)
	}
	if r.Blocks[0].StartLine != 3 || r.Blocks[0].PassedExecuting != 0 || r.Blocks[0].FailedExecuting != 1 || r.Blocks[0].Numerator != r.Blocks[0].Denominator {
		t.Fatal("failed branch not localized", r.Blocks)
	}
}
