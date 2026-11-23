package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/modelpolicy"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

func initCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	binary := flags.String("codex", "", "Codex executable")
	autoInit := flags.Bool("auto", false, "auto-detect best available local runtime (claude, api key, codex)")
	useClaude := flags.Bool("claude", false, "configure local Claude Code CLI (auto-discovered on PATH)")
	claudeBin := flags.String("claude-bin", "", "explicit Claude Code CLI executable path")
	acpBin := flags.String("acp", "", "Agent Client Protocol (ACP) stdio agent executable")
	acpArgs := flags.String("acp-args", "", "optional whitespace-separated arguments for ACP executable")
	apiDialect := flags.String("api", "", "direct HTTP API dialect (anthropic or openai)")
	apiBaseURL := flags.String("base-url", "", "custom base URL for --api (e.g. OpenRouter, Ollama, LiteLLM)")
	apiKeyEnv := flags.String("api-key-env", "", "environment variable name holding the API key for --api")
	model := flags.String("model", "", "model identifier")
	effort := flags.String("effort", "medium", "reasoning effort")
	writerModel := flags.String("writer-model", "", "writer model; defaults to --model")
	writerEffort := flags.String("writer-effort", "", "writer reasoning effort; defaults to --effort")
	reviewerModel := flags.String("reviewer-model", "", "reviewer model; defaults to --model")
	reviewerEffort := flags.String("reviewer-effort", "", "reviewer reasoning effort; defaults to --effort")
	fixerModel := flags.String("fixer-model", "", "independent fixer model; defaults to --model when either fixer override is supplied")
	fixerEffort := flags.String("fixer-effort", "", "independent fixer reasoning effort; defaults to --effort when either fixer override is supplied")
	accessConfigPath := flags.String("access-config", "", "strict JSON config.Access policy required when either fixer override is supplied")
	modelPolicyPath := flags.String("model-policy", "", "bounded strict JSON model policy embedded in configuration; requires a fixer override and access config")
	auth := flags.String("auth-source", "", "existing Codex auth.json")
	state := flags.String("state-root", "", "private runtime state directory")
	validateWriterEdits := flags.Bool("validate-writer-edits", false, "enable same-turn validation for anchored writer edits")
	strictWriterEdits := flags.Bool("strict-writer-edits", false, "enable strict same-turn validation and reject no-op anchored writer entries")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("init accepts flags only")
	}
	smartOpts := smartInitOptions{
		auto:       *autoInit,
		useClaude:  *useClaude,
		claudeBin:  *claudeBin,
		acpBin:     *acpBin,
		acpArgs:    *acpArgs,
		apiDialect: *apiDialect,
		apiBaseURL: *apiBaseURL,
		apiKeyEnv:  *apiKeyEnv,
		model:      *model,
		effort:     *effort,
	}
	if smartOpts.active() {
		return runSmartInit(ctx, root, smartOpts, out)
	}
	if *validateWriterEdits && *strictWriterEdits {
		return errors.New("--validate-writer-edits and --strict-writer-edits are mutually exclusive")
	}
	fixerProfileRequested := false
	fixerModelProvided, fixerEffortProvided, accessConfigProvided := false, false, false
	modelPolicyProvided := false
	flags.Visit(func(selected *flag.Flag) {
		switch selected.Name {
		case "fixer-model":
			fixerProfileRequested = true
			fixerModelProvided = true
		case "fixer-effort":
			fixerProfileRequested = true
			fixerEffortProvided = true
		case "access-config":
			accessConfigProvided = true
		case "model-policy":
			modelPolicyProvided = true
		}
	})
	if fixerProfileRequested != accessConfigProvided || (*accessConfigPath == "") != !accessConfigProvided {
		return errors.New("fixer-model or fixer-effort requires exactly one --access-config PATH; --access-config is only valid with a fixer override")
	}
	if fixerModelProvided && strings.TrimSpace(*fixerModel) == "" {
		return errors.New("fixer-model must not be empty")
	}
	if fixerEffortProvided && strings.TrimSpace(*fixerEffort) == "" {
		return errors.New("fixer-effort must not be empty")
	}
	if modelPolicyProvided && (!fixerProfileRequested || strings.TrimSpace(*modelPolicyPath) == "") {
		return errors.New("model-policy requires a nonempty path, a fixer override and access config")
	}
	var explicitModelPolicy *modelpolicy.Policy
	if modelPolicyProvided {
		var err error
		explicitModelPolicy, err = readInitModelPolicy(*modelPolicyPath)
		if err != nil {
			return err
		}
	}
	var explicitAccess *config.Access
	if accessConfigProvided {
		policy, err := readInitAccessConfig(*accessConfigPath)
		if err != nil {
			return err
		}
		explicitAccess = &policy
	}
	content := []byte(config.Example)
	runtimeName := "fake"
	if len(args) != 0 {
		if strings.TrimSpace(*binary) == "" || strings.TrimSpace(*model) == "" {
			return errors.New("real-model init requires --codex EXE and --model MODEL")
		}
		var err error
		*binary, err = filepath.Abs(*binary)
		if err != nil {
			return err
		}
		info, err := os.Lstat(*binary)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("--codex must name an existing regular executable file")
		}
		// Bind the selected binary without ever reading authentication contents.
		f, err := os.Open(*binary)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, hashErr := io.Copy(h, io.LimitReader(f, (512<<20)+1))
		if err := errors.Join(hashErr, f.Close()); err != nil || n > 512<<20 {
			return errors.New("Codex executable could not be hashed within its size bound")
		}
		if *auth == "" {
			home := os.Getenv("CODEX_HOME")
			if home == "" {
				userHome, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				home = filepath.Join(userHome, ".codex")
			}
			*auth = filepath.Join(home, "auth.json")
		}
		*auth, err = filepath.Abs(*auth)
		if err != nil {
			return err
		}
		info, err = os.Lstat(*auth)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("Codex authentication file unavailable; run codex login or supply --auth-source PATH")
		}
		if _, err := repository.Discover(ctx, root, filepath.Base(root)); err != nil {
			return fmt.Errorf("real-model init requires a committed Git repository: %w", err)
		}
		if *state == "" {
			cache, err := os.UserCacheDir()
			if err != nil {
				return err
			}
			id := sha256.Sum256([]byte(root))
			*state = filepath.Join(cache, "Fabric", "codex", hex.EncodeToString(id[:16]))
		}
		*state, err = filepath.Abs(*state)
		if err != nil {
			return err
		}
		if rel, err := filepath.Rel(root, *state); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("--state-root must be outside the project repository")
		}
		profile := func(role string) *runtime.Profile {
			selectedModel, selectedEffort := *model, *effort
			var overrideModel, overrideEffort string
			switch role {
			case "writer":
				overrideModel, overrideEffort = *writerModel, *writerEffort
			case "reviewer":
				overrideModel, overrideEffort = *reviewerModel, *reviewerEffort
			case "fixer":
				overrideModel, overrideEffort = *fixerModel, *fixerEffort
			}
			if overrideModel != "" {
				selectedModel = overrideModel
			}
			if overrideEffort != "" {
				selectedEffort = overrideEffort
			}
			return &runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: selectedModel, Effort: selectedEffort, Role: role}
		}
		writerContract := writercontract.ContractAnchoredEditsV1
		if *validateWriterEdits {
			writerContract = writercontract.ContractAnchoredEditsV2
		}
		if *strictWriterEdits {
			writerContract = writercontract.ContractAnchoredEditsV3
		}
		cfg := config.Config{
			Version: 1, Repository: filepath.Base(root), BaseBranch: "HEAD",
			WriterContract: writerContract, PlannerContract: "plan-v1", ExplorerContract: "json-v2",
			Planner: *profile("planner"), Explorer: profile("explorer"), Writer: profile("writer"), Reviewer: profile("reviewer"),
			Codex:        &config.Codex{Executable: *binary, ExecutableHash: hex.EncodeToString(h.Sum(nil)), StateRoot: *state, AuthSource: *auth},
			Verification: []config.Check{{Name: "unit", Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 120}},
		}
		if fixerProfileRequested {
			cfg.Version = 2
			cfg.Fixer = profile("fixer")
			cfg.Access = explicitAccess
		}
		cfg.ModelPolicy = explicitModelPolicy
		if err := cfg.Validate(); err != nil {
			if fixerProfileRequested {
				return fmt.Errorf("fixer init requires an access config admitting every configured role and budget: %w", err)
			}
			return err
		}
		if fixerProfileRequested {
			if _, err := cfg.AccessPolicy(strings.Repeat("0", 64)); err != nil {
				return fmt.Errorf("fixer route admission validation failed: %w", err)
			}
		}
		content, err = toml.Marshal(cfg)
		if err != nil {
			return err
		}
		// Do not create runtime state if the project configuration already exists.
		if _, err := os.Lstat(filepath.Join(root, "harness.toml")); !os.IsNotExist(err) {
			return errors.New("harness.toml already exists; init never overwrites configuration")
		}
		if err := os.MkdirAll(*state, 0700); err != nil {
			return err
		}
		runtimeName = "codex-app-server"
	}
	f, err := os.OpenFile(filepath.Join(root, "harness.toml"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
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
	return output(out, map[string]string{"status": "CREATED", "configuration": "harness.toml", "runtime": runtimeName, "next": "ignore .harness/ and harness.toml; edit verification checks for your project; run doctor"})
}

const initAccessConfigMaxBytes = 32 << 10

func readInitAccessConfig(path string) (config.Access, error) {
	var policy config.Access
	raw, err := readRegularPolicyJSON(path, initAccessConfigMaxBytes, "access config", "32 KiB")
	if err != nil {
		return policy, err
	}
	if err := canonical.Decode(raw, &policy); err != nil {
		return config.Access{}, fmt.Errorf("invalid access config JSON: %w", err)
	}
	return policy, nil
}
