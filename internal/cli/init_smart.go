package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/iancuuandrei/fabric/internal/acpruntime"
	"github.com/iancuuandrei/fabric/internal/clauderuntime"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/directapi"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/writercontract"
	"github.com/pelletier/go-toml/v2"
)

type smartInitOptions struct {
	auto       bool
	useClaude  bool
	claudeBin  string
	acpBin     string
	acpArgs    string
	apiDialect string
	apiBaseURL string
	apiKeyEnv  string
	model      string
	effort     string
}

func (o smartInitOptions) active() bool {
	return o.auto || o.useClaude || o.claudeBin != "" || o.acpBin != "" || o.apiDialect != ""
}

// detectProjectChecks infers sensible default native verification commands
// from standard project manifests so non-Go repositories work out of the box.
func detectProjectChecks(root string) []config.Check {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(root, name))
		return err == nil
	}
	switch {
	case exists("go.mod"):
		return []config.Check{{Name: "unit", Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 180}}
	case exists("Cargo.toml"):
		return []config.Check{{Name: "unit", Argv: []string{"cargo", "test", "--workspace"}, TimeoutSeconds: 300}}
	case exists("package.json"):
		if exists("pnpm-lock.yaml") {
			return []config.Check{{Name: "unit", Argv: []string{"pnpm", "test"}, TimeoutSeconds: 180}}
		}
		return []config.Check{{Name: "unit", Argv: []string{"npm", "test"}, TimeoutSeconds: 180}}
	case exists("pyproject.toml") || exists("pytest.ini") || exists("setup.py"):
		return []config.Check{{Name: "unit", Argv: []string{"pytest"}, TimeoutSeconds: 180}}
	default:
		return []config.Check{{Name: "git-status", Argv: []string{"git", "status", "--short"}, TimeoutSeconds: 30}}
	}
}

func runSmartInit(ctx context.Context, root string, opts smartInitOptions, out io.Writer) error {
	for _, existing := range []string{"fabric.toml", "harness.toml"} {
		if _, err := os.Lstat(filepath.Join(root, existing)); !os.IsNotExist(err) {
			return fmt.Errorf("%s already exists; init never overwrites configuration", existing)
		}
	}
	if _, err := repository.Discover(ctx, root, filepath.Base(root)); err != nil {
		return fmt.Errorf("init requires a committed Git repository: %w", err)
	}

	effort := strings.TrimSpace(opts.effort)
	if effort == "" {
		effort = "medium"
	}

	if opts.auto {
		if bin, _, err := clauderuntime.DiscoverExecutable(); err == nil {
			opts.useClaude = true
			opts.claudeBin = bin
		} else if os.Getenv("ANTHROPIC_API_KEY") != "" {
			opts.apiDialect = directapi.DialectAnthropic
		} else if os.Getenv("OPENAI_API_KEY") != "" {
			opts.apiDialect = directapi.DialectOpenAI
		} else if codexBin, err := exec.LookPath("codex"); err == nil {
			return fmt.Errorf("detected codex at %s; run `fabric init --codex %s --model <MODEL>`", codexBin, codexBin)
		} else {
			return errors.New("no local runtime auto-detected; install claude/codex or set ANTHROPIC_API_KEY / OPENAI_API_KEY, or pass --claude / --api / --acp")
		}
	}

	var cfg config.Config
	var runtimeName string

	makeProfiles := func(rt, prov, mdl string) {
		p := func(role string) *runtime.Profile {
			return &runtime.Profile{Runtime: rt, Provider: prov, Model: mdl, Effort: effort, Role: role}
		}
		cfg = config.Config{
			Version:          1,
			Repository:       filepath.Base(root),
			BaseBranch:       "HEAD",
			WriterContract:   writercontract.ContractAnchoredEditsV1,
			PlannerContract:  "plan-v1",
			ExplorerContract: "json-v2",
			ReviewerContract: "json-v1",
			Planner:          *p("planner"),
			Explorer:         p("explorer"),
			Writer:           p("writer"),
			Reviewer:         p("reviewer"),
			Verification:     detectProjectChecks(root),
		}
	}

	switch {
	case opts.useClaude || opts.claudeBin != "":
		bin := opts.claudeBin
		var hash string
		var err error
		if bin == "" {
			bin, hash, err = clauderuntime.DiscoverExecutable()
			if err != nil {
				return err
			}
		} else {
			bin, err = filepath.Abs(bin)
			if err != nil {
				return err
			}
			hash, err = clauderuntime.HashFile(bin)
			if err != nil {
				return fmt.Errorf("hash claude executable: %w", err)
			}
		}
		model := strings.TrimSpace(opts.model)
		if model == "" {
			model = "claude-sonnet-4-5"
		}
		makeProfiles(clauderuntime.RuntimeName, "anthropic", model)
		cfg.Claude = &config.ClaudeHost{Executable: bin, ExecutableHash: hash, TimeoutSeconds: 600}
		runtimeName = clauderuntime.RuntimeName

	case opts.acpBin != "":
		bin, err := filepath.Abs(opts.acpBin)
		if err != nil {
			return err
		}
		hash, err := acpruntime.HashFile(bin)
		if err != nil {
			return fmt.Errorf("hash acp executable: %w", err)
		}
		model := strings.TrimSpace(opts.model)
		if model == "" {
			model = "default"
		}
		var acpArgs []string
		if strings.TrimSpace(opts.acpArgs) != "" {
			acpArgs = strings.Fields(opts.acpArgs)
		}
		makeProfiles(acpruntime.RuntimeName, "acp", model)
		cfg.ACP = &config.ACPHost{Executable: bin, ExecutableHash: hash, Args: acpArgs, TimeoutSeconds: 600}
		runtimeName = acpruntime.RuntimeName

	case opts.apiDialect != "":
		dialect := strings.ToLower(strings.TrimSpace(opts.apiDialect))
		if dialect != directapi.DialectAnthropic && dialect != directapi.DialectOpenAI {
			return fmt.Errorf("--api must be 'anthropic' or 'openai', got %q", opts.apiDialect)
		}
		model := strings.TrimSpace(opts.model)
		if model == "" {
			if dialect == directapi.DialectAnthropic {
				model = "claude-sonnet-4-5"
			} else {
				model = "gpt-5"
			}
		}
		keyEnv := strings.TrimSpace(opts.apiKeyEnv)
		if keyEnv == "" {
			keyEnv = directapi.DefaultAPIKeyEnv(dialect)
		}
		baseURL := strings.TrimSpace(opts.apiBaseURL)
		if baseURL == "" {
			baseURL = directapi.DefaultBaseURL(dialect)
		}
		makeProfiles(directapi.RuntimeName, dialect, model)
		cfg.DirectAPI = &config.DirectAPI{
			Dialect:        dialect,
			BaseURL:        baseURL,
			APIKeyEnv:      keyEnv,
			TimeoutSeconds: 300,
		}
		runtimeName = directapi.RuntimeName
	default:
		return errors.New("no runtime selected")
	}

	if err := cfg.Validate(); err != nil {
		return err
	}
	content, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	target := filepath.Join(root, "fabric.toml")
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(content)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	return output(out, map[string]string{
		"status":        "CREATED",
		"configuration": "fabric.toml",
		"runtime":       runtimeName,
		"next":          "add .harness/ and fabric.toml to .git/info/exclude; run `fabric doctor`; then `fabric run --autonomous \"your objective\"`",
	})
}
