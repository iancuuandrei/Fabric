package verification

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/config"
)

func TestVerificationChild(t *testing.T) {
	mode := ""
	for index, arg := range os.Args {
		if arg == "--" && index+1 < len(os.Args) {
			mode = os.Args[index+1]
			break
		}
	}
	switch mode {
	case "success":
		os.Stdout.Write([]byte("hello\xff\n"))
		os.Stderr.Write([]byte("warning\n"))
		os.Exit(0)
	case "failure":
		os.Exit(7)
	case "timeout":
		time.Sleep(10 * time.Second)
		os.Exit(0)
	case "overflow":
		os.Stdout.Write(bytes.Repeat([]byte("x"), 5<<20))
		os.Exit(0)
	case "environment":
		if os.Getenv("HARNESS_TEST_SECRET") != "" {
			os.Exit(9)
		}
		os.Exit(0)
	}
}

func invocation(t *testing.T, mode string) Invocation {
	t.Helper()
	i, err := Prepare(strings.Repeat("a", 64), t.TempDir(), config.Check{Name: mode, Argv: []string{os.Args[0], "-test.run=^TestVerificationChild$", "--", mode}, TimeoutSeconds: 2})
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestExecutedResultsAndOutputHashes(t *testing.T) {
	for _, mode := range []string{"success", "failure", "environment"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HARNESS_TEST_SECRET", "must-not-leak")
			i := invocation(t, mode)
			r, err := Execute(context.Background(), i)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateResult(i, r); err != nil {
				t.Fatal(err)
			}
			want := "PASS"
			if mode == "failure" {
				want = "FAIL"
			}
			if r.Status != want || !r.Started {
				t.Fatal(r)
			}
			if mode == "success" {
				h := sha256.Sum256([]byte("hello\xff\n"))
				if r.Stdout.Hash != hex.EncodeToString(h[:]) || r.Stdout.Excerpt != "hello�\n" {
					t.Fatal("output identity/rendering mismatch")
				}
			}
			r.Status = "PASS"
			if mode == "failure" && ValidateResult(i, r) == nil {
				t.Fatal("false PASS accepted")
			}
		})
	}
}

func TestMissingToolAndCancellationNeverPass(t *testing.T) {
	i, err := Prepare(strings.Repeat("a", 64), t.TempDir(), config.Check{Name: "missing", Argv: []string{"harness-no-such-executable-987643"}, TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Execute(context.Background(), i)
	if err != nil || r.Started || r.Status != "NOT_RUN" {
		t.Fatal(r, err)
	}
	if err := ValidateResult(i, r); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	i = invocation(t, "success")
	r, err = Execute(ctx, i)
	if err != nil || !r.Cancelled || r.Status != "NOT_RUN" {
		t.Fatal(r, err)
	}
	if _, err := Prepare(strings.Repeat("a", 64), t.TempDir(), config.Check{}); err == nil {
		t.Fatal("empty argv accepted")
	}
}

func TestTimeoutAndOutputBounds(t *testing.T) {
	for _, mode := range []string{"timeout", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			i := invocation(t, mode)
			i.Check.TimeoutSeconds = 1
			started := time.Now()
			r, err := Execute(context.Background(), i)
			if err != nil {
				t.Fatal(err)
			}
			if time.Since(started) > 9*time.Second {
				t.Fatal("process termination exceeded bound")
			}
			if r.Status != "FAIL" {
				t.Fatal(r)
			}
			if mode == "timeout" && !r.TimedOut {
				t.Fatal("timeout not recorded")
			}
			if mode == "overflow" && (!r.Stdout.Truncated || r.Stdout.CapturedBytes != 4<<20) {
				t.Fatal("overflow not bounded")
			}
			if err := ValidateResult(i, r); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEnvironmentAndExecutableDriftRejected(t *testing.T) {
	i := invocation(t, "success")
	i.Environment["UNDECLARED_SECRET"] = "x"
	if _, err := Execute(context.Background(), i); err == nil {
		t.Fatal("unknown environment admitted")
	}
	i = invocation(t, "success")
	i.Executable.Hash = strings.Repeat("0", 64)
	r, err := Execute(context.Background(), i)
	if err != nil || r.Status != "NOT_RUN" || r.Started {
		t.Fatal(r, err)
	}
}
